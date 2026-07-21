package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/domain/policy"
)

// anticheatError keeps database error text off the wire: the detectors read a lot of internal
// structure, and a leaked query error would describe it. Anything that fails is logged in full and
// returned as a bare 500.
func (s *Server) anticheatError(ctx context.Context, err error, action string) error {
	s.opts.Log.ErrorContext(ctx, action+" failed", "error", err)
	return huma.Error500InternalServerError("could not " + action)
}

type acSharingPairBody struct {
	IssuedTo        int64     `json:"issued_to"`
	IssuedToName    string    `json:"issued_to_name"`
	Submitter       int64     `json:"submitter"`
	SubmitterName   string    `json:"submitter_name"`
	SubmissionCount int64     `json:"submission_count"`
	ChallengeCount  int64     `json:"challenge_count"`
	ChallengeIDs    []int64   `json:"challenge_ids"`
	FirstSeen       time.Time `json:"first_seen"`
	LastSeen        time.Time `json:"last_seen"`
}

type acFlagSharingInput struct {
	Page    int `query:"page" minimum:"1" maximum:"1000000" default:"1"`
	PerPage int `query:"per_page" minimum:"1" maximum:"100" default:"50"`
}

type acFlagSharingOutput struct {
	Body struct {
		Pairs   []acSharingPairBody `json:"pairs"`
		Total   int64               `json:"total"`
		Page    int                 `json:"page"`
		PerPage int                 `json:"per_page"`
	}
}

// acIPClusterBody keeps AccountIDs and AccountNames index-aligned: AccountNames[i] names AccountIDs[i].
type acIPClusterBody struct {
	IP           string    `json:"ip"`
	AccountCount int64     `json:"account_count"`
	AccountIDs   []int64   `json:"account_ids"`
	AccountNames []string  `json:"account_names"`
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
}

type acIPOverlapInput struct {
	// A cluster is a signal, not a verdict: shared NAT and campus egress put unrelated players on one
	// address, so the threshold is the operator's to tune. Two distinct accounts is the loosest it
	// can be and still mean "shared".
	MinAccounts int `query:"min_accounts" minimum:"2" maximum:"1000" default:"2"`
	Page        int `query:"page" minimum:"1" maximum:"1000000" default:"1"`
	PerPage     int `query:"per_page" minimum:"1" maximum:"100" default:"50"`
}

type acIPOverlapOutput struct {
	Body struct {
		Clusters    []acIPClusterBody `json:"clusters"`
		Total       int64             `json:"total"`
		MinAccounts int               `json:"min_accounts"`
		Page        int               `json:"page"`
		PerPage     int               `json:"per_page"`
	}
}

// acUnissuedSolveBody carries both ids: the account is the unit the detector fires on, and the
// user is who to talk to — in teams mode they are different rows, and an admin needs both.
type acUnissuedSolveBody struct {
	SolveID       int64     `json:"solve_id"`
	ChallengeID   int64     `json:"challenge_id"`
	ChallengeName string    `json:"challenge_name"`
	AccountID     int64     `json:"account_id"`
	UserID        int64     `json:"user_id"`
	UserName      string    `json:"user_name"`
	TeamID        *int64    `json:"team_id,omitempty"`
	TeamName      *string   `json:"team_name,omitempty"`
	Date          time.Time `json:"date"`
	Value         int32     `json:"value"`
}

type acUnissuedSolvesInput struct {
	Page    int `query:"page" minimum:"1" maximum:"1000000" default:"1"`
	PerPage int `query:"per_page" minimum:"1" maximum:"100" default:"50"`
}

type acUnissuedSolvesOutput struct {
	Body struct {
		Solves  []acUnissuedSolveBody `json:"solves"`
		Total   int64                 `json:"total"`
		Page    int                   `json:"page"`
		PerPage int                   `json:"per_page"`
	}
}

type acSharingEdgeBody struct {
	Direction        string    `json:"direction"`
	Counterparty     int64     `json:"counterparty"`
	CounterpartyName string    `json:"counterparty_name"`
	SubmissionCount  int64     `json:"submission_count"`
	ChallengeIDs     []int64   `json:"challenge_ids"`
	FirstSeen        time.Time `json:"first_seen"`
	LastSeen         time.Time `json:"last_seen"`
}

type acIPNeighborBody struct {
	IP               string    `json:"ip"`
	OtherAccountID   int64     `json:"other_account_id"`
	OtherAccountName string    `json:"other_account_name"`
	SubmissionCount  int64     `json:"submission_count"`
	FirstSeen        time.Time `json:"first_seen"`
	LastSeen         time.Time `json:"last_seen"`
}

type acAccountInput struct {
	ID int64 `path:"id"`
}

type acAccountReportOutput struct {
	Body struct {
		AccountID   int64               `json:"account_id"`
		AccountName string              `json:"account_name"`
		Sharing     []acSharingEdgeBody `json:"sharing"`
		IPOverlap   []acIPNeighborBody  `json:"ip_overlap"`
	}
}

func (s *Server) registerAdminAnticheat() {
	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-anticheat-flag-sharing", Method: http.MethodGet, Path: "/anticheat/flag-sharing",
		Summary: "Flag-sharing evidence grouped by offender pair (paginated)", Tags: []string{"admin/anticheat"},
	}, s.adminAnticheatFlagSharing)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-anticheat-ip-overlap", Method: http.MethodGet, Path: "/anticheat/ip-overlap",
		Summary: "Accounts sharing a submission IP (signal, not verdict; paginated)", Tags: []string{"admin/anticheat"},
	}, s.adminAnticheatIPOverlap)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-anticheat-unissued-solves", Method: http.MethodGet, Path: "/anticheat/unissued-solves",
		Summary: "Solves on unique-flag challenges by accounts never issued an instance (paginated)",
		Tags:    []string{"admin/anticheat"},
	}, s.adminAnticheatUnissuedSolves)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-anticheat-account", Method: http.MethodGet, Path: "/anticheat/accounts/{id}",
		Summary: "One account's sharing and IP-overlap evidence", Tags: []string{"admin/anticheat"},
	}, s.adminAnticheatAccount)
}

func (s *Server) adminAnticheatUnissuedSolves(ctx context.Context, in *acUnissuedSolvesInput) (*acUnissuedSolvesOutput, error) {
	page, err := s.opts.Anticheat.UnissuedSolves(ctx, in.Page, in.PerPage)
	if err != nil {
		return nil, s.anticheatError(ctx, err, "list unissued-solve evidence")
	}
	out := &acUnissuedSolvesOutput{}
	out.Body.Total = page.Total
	out.Body.Page = in.Page
	out.Body.PerPage = in.PerPage
	out.Body.Solves = make([]acUnissuedSolveBody, len(page.Solves))
	for i := range page.Solves {
		u := &page.Solves[i]
		out.Body.Solves[i] = acUnissuedSolveBody{
			SolveID: u.SolveID, ChallengeID: u.ChallengeID, ChallengeName: u.ChallengeName,
			AccountID: u.AccountID, UserID: u.UserID, UserName: u.UserName,
			TeamID: u.TeamID, TeamName: u.TeamName,
			Date: u.Date, Value: u.Value,
		}
	}
	return out, nil
}

func (s *Server) adminAnticheatFlagSharing(ctx context.Context, in *acFlagSharingInput) (*acFlagSharingOutput, error) {
	page, err := s.opts.Anticheat.FlagSharing(ctx, in.Page, in.PerPage)
	if err != nil {
		return nil, s.anticheatError(ctx, err, "list flag-sharing evidence")
	}
	out := &acFlagSharingOutput{}
	out.Body.Total = page.Total
	out.Body.Page = in.Page
	out.Body.PerPage = in.PerPage
	out.Body.Pairs = make([]acSharingPairBody, len(page.Pairs))
	for i := range page.Pairs {
		p := &page.Pairs[i]
		out.Body.Pairs[i] = acSharingPairBody{
			IssuedTo: p.IssuedTo, IssuedToName: p.IssuedToName,
			Submitter: p.Submitter, SubmitterName: p.SubmitterName,
			SubmissionCount: p.SubmissionCount, ChallengeCount: p.ChallengeCount,
			ChallengeIDs: p.ChallengeIDs, FirstSeen: p.FirstSeen, LastSeen: p.LastSeen,
		}
	}
	return out, nil
}

func (s *Server) adminAnticheatIPOverlap(ctx context.Context, in *acIPOverlapInput) (*acIPOverlapOutput, error) {
	page, err := s.opts.Anticheat.IPOverlap(ctx, in.MinAccounts, in.Page, in.PerPage)
	if err != nil {
		return nil, s.anticheatError(ctx, err, "list ip-overlap evidence")
	}
	out := &acIPOverlapOutput{}
	out.Body.Total = page.Total
	out.Body.MinAccounts = in.MinAccounts
	out.Body.Page = in.Page
	out.Body.PerPage = in.PerPage
	out.Body.Clusters = make([]acIPClusterBody, len(page.Clusters))
	for i := range page.Clusters {
		c := &page.Clusters[i]
		out.Body.Clusters[i] = acIPClusterBody{
			IP: c.IP.String(), AccountCount: c.AccountCount,
			AccountIDs: c.AccountIDs, AccountNames: c.AccountNames,
			FirstSeen: c.FirstSeen, LastSeen: c.LastSeen,
		}
	}
	return out, nil
}

func (s *Server) adminAnticheatAccount(ctx context.Context, in *acAccountInput) (*acAccountReportOutput, error) {
	rep, err := s.opts.Anticheat.AccountReport(ctx, in.ID)
	if err != nil {
		return nil, s.anticheatError(ctx, err, "build account anti-cheat report")
	}
	out := &acAccountReportOutput{}
	out.Body.AccountID = rep.AccountID
	out.Body.AccountName = rep.AccountName
	out.Body.Sharing = make([]acSharingEdgeBody, len(rep.Sharing))
	for i := range rep.Sharing {
		e := &rep.Sharing[i]
		out.Body.Sharing[i] = acSharingEdgeBody{
			Direction: e.Direction, Counterparty: e.Counterparty, CounterpartyName: e.CounterpartyName,
			SubmissionCount: e.SubmissionCount, ChallengeIDs: e.ChallengeIDs,
			FirstSeen: e.FirstSeen, LastSeen: e.LastSeen,
		}
	}
	out.Body.IPOverlap = make([]acIPNeighborBody, len(rep.IPOverlap))
	for i := range rep.IPOverlap {
		n := &rep.IPOverlap[i]
		out.Body.IPOverlap[i] = acIPNeighborBody{
			IP: n.IP.String(), OtherAccountID: n.OtherAccountID, OtherAccountName: n.OtherAccountName,
			SubmissionCount: n.SubmissionCount, FirstSeen: n.FirstSeen, LastSeen: n.LastSeen,
		}
	}
	return out, nil
}
