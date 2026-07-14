// Package anticheat is the read side of the cheat-detection surface: it turns the evidence the
// submit path already stamped — the flag each correct answer was attributed to, and the address it
// came from — into reports an admin reviews. It holds no locks, mutates nothing, and never touches
// the hot path. Every result here is a signal for a human, not a verdict: nothing in this package
// penalises an account.
package anticheat

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/starvy/flagfish/internal/db"
)

var Module = fx.Module("anticheat", fx.Provide(New))

type Service struct {
	q *db.Queries
}

func New(pool *pgxpool.Pool) *Service { return &Service{q: db.New(pool)} }

// accountEvidenceLimit caps the per-account IP-overlap list. One account's evidence is bounded in
// practice; the limit only guards against a pathological account that shared an address with the
// whole event.
const accountEvidenceLimit = 500

// SharingPair is one offender pair: account Submitter submitted correct flags that were issued to
// account IssuedTo. The counts and challenge span fold repeated sharing into a single row.
type SharingPair struct {
	IssuedTo        int64
	Submitter       int64
	SubmissionCount int64
	ChallengeCount  int64
	ChallengeIDs    []int64
	FirstSeen       time.Time
	LastSeen        time.Time
}

// SharingPage is one page of sharing pairs plus the total the pagination is computed from.
type SharingPage struct {
	Pairs []SharingPair
	Total int64
}

// IPCluster is one address several distinct accounts submitted from.
type IPCluster struct {
	IP           netip.Addr
	AccountCount int64
	AccountIDs   []int64
	FirstSeen    time.Time
	LastSeen     time.Time
}

// IPOverlapPage is one page of IP clusters plus the total.
type IPOverlapPage struct {
	Clusters []IPCluster
	Total    int64
}

// SharingEdge is one counterparty in a single account's sharing evidence. Direction is "issued_to"
// when the subject account was issued the flag and someone else submitted it, and "submitted" when
// the subject submitted a flag issued to the counterparty.
type SharingEdge struct {
	Direction       string
	Counterparty    int64
	SubmissionCount int64
	ChallengeIDs    []int64
	FirstSeen       time.Time
	LastSeen        time.Time
}

// IPNeighbor is one other account that shared an address with the subject account.
type IPNeighbor struct {
	IP              netip.Addr
	OtherAccountID  int64
	SubmissionCount int64
	FirstSeen       time.Time
	LastSeen        time.Time
}

// AccountReport is the sharing and IP-overlap evidence touching one account.
type AccountReport struct {
	AccountID int64
	Sharing   []SharingEdge
	IPOverlap []IPNeighbor
}

// FlagSharing returns cross-account flag submissions grouped by offender pair, one page at a time.
func (s *Service) FlagSharing(ctx context.Context, page, perPage int) (SharingPage, error) {
	total, err := s.q.CountFlagSharingPairs(ctx)
	if err != nil {
		return SharingPage{}, fmt.Errorf("anticheat: count flag-sharing pairs: %w", err)
	}
	rows, err := s.q.FindFlagSharingPairs(ctx, db.FindFlagSharingPairsParams{
		Lim: int32(perPage),              //nolint:gosec // per_page is capped by the handler
		Off: int32((page - 1) * perPage), //nolint:gosec // page is bounded by the handler
	})
	if err != nil {
		return SharingPage{}, fmt.Errorf("anticheat: flag-sharing pairs: %w", err)
	}
	out := SharingPage{Total: total, Pairs: make([]SharingPair, len(rows))}
	for i, r := range rows {
		out.Pairs[i] = SharingPair{
			IssuedTo: r.IssuedTo, Submitter: r.Submitter,
			SubmissionCount: r.SubmissionCount, ChallengeCount: r.ChallengeCount,
			ChallengeIDs: r.ChallengeIds,
			FirstSeen:    r.FirstSeen.Time, LastSeen: r.LastSeen.Time,
		}
	}
	return out, nil
}

// IPOverlap returns addresses used by at least minAccounts distinct accounts, one page at a time.
func (s *Service) IPOverlap(ctx context.Context, minAccounts, page, perPage int) (IPOverlapPage, error) {
	total, err := s.q.CountIPOverlaps(ctx, int32(minAccounts)) //nolint:gosec // min_accounts is bounded by the handler
	if err != nil {
		return IPOverlapPage{}, fmt.Errorf("anticheat: count ip overlaps: %w", err)
	}
	rows, err := s.q.FindIPOverlaps(ctx, db.FindIPOverlapsParams{
		MinAccounts: int32(minAccounts),          //nolint:gosec // min_accounts is bounded by the handler
		Lim:         int32(perPage),              //nolint:gosec // per_page is capped by the handler
		Off:         int32((page - 1) * perPage), //nolint:gosec // page is bounded by the handler
	})
	if err != nil {
		return IPOverlapPage{}, fmt.Errorf("anticheat: ip overlaps: %w", err)
	}
	out := IPOverlapPage{Total: total, Clusters: make([]IPCluster, len(rows))}
	for i, r := range rows {
		out.Clusters[i] = IPCluster{
			IP: derefAddr(r.Ip), AccountCount: r.AccountCount, AccountIDs: r.AccountIds,
			FirstSeen: r.FirstSeen.Time, LastSeen: r.LastSeen.Time,
		}
	}
	return out, nil
}

// AccountReport gathers one account's sharing and IP-overlap evidence. An account with no evidence,
// and an account that does not exist, both return an empty report — this is analytics over the
// submissions log, not an account lookup.
func (s *Service) AccountReport(ctx context.Context, accountID int64) (AccountReport, error) {
	shares, err := s.q.FindFlagSharingForAccount(ctx, accountID)
	if err != nil {
		return AccountReport{}, fmt.Errorf("anticheat: account %d sharing: %w", accountID, err)
	}
	neighbors, err := s.q.FindIPOverlapForAccount(ctx, db.FindIPOverlapForAccountParams{
		AccountID: accountID, Lim: accountEvidenceLimit,
	})
	if err != nil {
		return AccountReport{}, fmt.Errorf("anticheat: account %d ip overlap: %w", accountID, err)
	}
	rep := AccountReport{
		AccountID: accountID,
		Sharing:   make([]SharingEdge, len(shares)),
		IPOverlap: make([]IPNeighbor, len(neighbors)),
	}
	for i, r := range shares {
		rep.Sharing[i] = SharingEdge{
			Direction: r.Direction, Counterparty: r.Counterparty,
			SubmissionCount: r.SubmissionCount, ChallengeIDs: r.ChallengeIds,
			FirstSeen: r.FirstSeen.Time, LastSeen: r.LastSeen.Time,
		}
	}
	for i, r := range neighbors {
		rep.IPOverlap[i] = IPNeighbor{
			IP: derefAddr(r.Ip), OtherAccountID: r.OtherAccountID,
			SubmissionCount: r.SubmissionCount, FirstSeen: r.FirstSeen.Time, LastSeen: r.LastSeen.Time,
		}
	}
	return rep, nil
}

// derefAddr unwraps the nullable address column. The queries filter ip IS NOT NULL, so a nil here
// would be a query regression rather than data; the zero Addr keeps that loud instead of panicking.
func derefAddr(a *netip.Addr) netip.Addr {
	if a == nil {
		return netip.Addr{}
	}
	return *a
}
