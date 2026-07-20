package adminops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/starvy/flagfish/internal/audit"
	"github.com/starvy/flagfish/internal/db"
)

// NewInstance is one pool entry the client uploads.
//
// There is deliberately no plaintext flag field: the client hashes the flag and sends only the
// digest, so the platform never holds a flag it could leak. Vars are handed to the owner verbatim
// and must never carry the flag.
type NewInstance struct {
	// ValueHash is sha256(flag). The transport decodes the client's hex into these 32 bytes before
	// the service sees it, so a malformed hash is a 4xx at the edge, never a short row in the table.
	ValueHash [32]byte
	// ArtifactID is an optional per-account file; when set it must belong to this challenge.
	ArtifactID *int64
	// Vars is arbitrary per-account JSON the description template renders against. Empty means {}.
	Vars json.RawMessage
}

// PoolUpload is a whole pool as one batch. It is applied atomically: the entire set lands in a new
// generation, or nothing does.
type PoolUpload struct {
	Instances []NewInstance
}

// PoolResult reports what an upload did. An idempotent re-upload of the newest generation's exact
// hash set writes nothing and reports that generation unchanged.
type PoolResult struct {
	Generation int32
	Inserted   int
	Idempotent bool
	// Warnings flag stored-but-suspect states the upload succeeded despite — e.g. a pool on a
	// challenge that is not in unique mode, so the instances will never be issued.
	Warnings []string
}

// ReplacePool uploads a challenge's instance pool as a new generation.
//
// It does not delete the old pool: re-uploading bumps the generation, and AssignInstance prefers the
// newest, so new players draw from the new set while everyone already issued from a prior generation
// stays attributable to the instance they hold. Idempotency makes a repeated push a no-op: an upload
// whose hash set equals the newest generation's returns that generation untouched, so a sync tool can
// push the same pool twice without doubling it.
func (s *Service) ReplacePool(ctx context.Context, actor audit.Actor, challengeID int64, up PoolUpload) (PoolResult, error) {
	if len(up.Instances) == 0 {
		return PoolResult{}, invalidf("a pool upload must contain at least one instance")
	}

	// Reject an in-batch duplicate here for a readable message; UNIQUE(challenge_id, value_hash,
	// generation) is the actual arbiter and would roll the whole upload back regardless.
	seen := make(map[[32]byte]struct{}, len(up.Instances))
	for i, inst := range up.Instances {
		if _, dup := seen[inst.ValueHash]; dup {
			return PoolResult{}, invalidf("instance %d repeats a flag hash already in this upload", i)
		}
		seen[inst.ValueHash] = struct{}{}
		if len(inst.Vars) > 0 && !json.Valid(inst.Vars) {
			return PoolResult{}, invalidf("instance %d has vars that are not valid JSON", i)
		}
	}

	var out PoolResult
	err := s.tx(ctx, actor, func(_ pgx.Tx, q *db.Queries) error {
		ch, err := q.AdminGetChallenge(ctx, challengeID)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: id=%d", ErrChallengeNotFound, challengeID)
		} else if err != nil {
			return fmt.Errorf("adminops: replace pool: read challenge %d: %w", challengeID, err)
		}

		if verr := validateArtifacts(ctx, q, challengeID, up.Instances); verr != nil {
			return verr
		}

		// Idempotent push: the newest generation already is exactly this set.
		newest, err := q.AdminNewestPoolHashes(ctx, challengeID)
		if err != nil {
			return fmt.Errorf("adminops: replace pool: read newest generation: %w", err)
		}
		if sameHashSet(newest, up.Instances) {
			gen, genErr := q.AdminNextPoolGeneration(ctx, challengeID)
			if genErr != nil {
				return fmt.Errorf("adminops: replace pool: read generation: %w", genErr)
			}
			out = PoolResult{Generation: gen - 1, Inserted: 0, Idempotent: true}
			out.Warnings = poolWarnings(ch.FlagMode)
			return nil
		}

		gen, err := q.AdminNextPoolGeneration(ctx, challengeID)
		if err != nil {
			return fmt.Errorf("adminops: replace pool: next generation: %w", err)
		}

		hashes := make([][]byte, len(up.Instances))
		artifacts := make([]int64, len(up.Instances))
		vars := make([]json.RawMessage, len(up.Instances))
		for i, inst := range up.Instances {
			h := inst.ValueHash
			hashes[i] = h[:]
			if inst.ArtifactID != nil {
				artifacts[i] = *inst.ArtifactID
			}
			if len(inst.Vars) > 0 {
				vars[i] = inst.Vars
			} else {
				vars[i] = json.RawMessage("{}")
			}
		}

		n, err := q.AdminInsertInstances(ctx, db.AdminInsertInstancesParams{
			ChallengeID: challengeID, Generation: gen,
			ValueHashes: hashes, ArtifactIds: artifacts, Vars: vars,
		})
		if err != nil {
			return fmt.Errorf("adminops: replace pool: insert %d instances: %w", len(up.Instances), err)
		}
		out = PoolResult{Generation: gen, Inserted: int(n)}
		out.Warnings = poolWarnings(ch.FlagMode)
		return nil
	})
	return out, err
}

// poolWarnings names a stored-but-suspect state the upload landed into: a pool on a challenge that is
// not in unique mode is inert until the mode is switched, and saying so beats letting an author
// wonder why their instances never issue.
func poolWarnings(flagMode string) []string {
	if flagMode != "unique" {
		return []string{"this challenge's flag_mode is '" + flagMode + "', so these instances will not be issued until it is switched to 'unique'"}
	}
	return nil
}

// validateArtifacts refuses an artifact_id that is not a file of this challenge — a per-account
// artifact from another challenge is either a mistake or a cross-challenge leak, never intent.
func validateArtifacts(ctx context.Context, q *db.Queries, challengeID int64, instances []NewInstance) error {
	want := make(map[int64]struct{})
	for _, inst := range instances {
		if inst.ArtifactID != nil {
			want[*inst.ArtifactID] = struct{}{}
		}
	}
	if len(want) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	found, err := q.AdminFilterChallengeFileIDs(ctx, db.AdminFilterChallengeFileIDsParams{
		Ids: ids, ChallengeID: &challengeID,
	})
	if err != nil {
		return fmt.Errorf("adminops: replace pool: validate artifacts: %w", err)
	}
	for _, id := range found {
		delete(want, id)
	}
	for id := range want {
		return invalidf("artifact %d is not a file of challenge %d", id, challengeID)
	}
	return nil
}

// sameHashSet reports whether the uploaded instances' hashes are exactly the newest generation's
// set. Order does not matter; a pool is a set, and re-uploading the same flags in a different order
// is still the same pool.
func sameHashSet(newest [][]byte, instances []NewInstance) bool {
	if len(newest) != len(instances) {
		return false
	}
	set := make(map[[32]byte]struct{}, len(newest))
	for _, h := range newest {
		if len(h) != 32 {
			return false
		}
		var k [32]byte
		copy(k[:], h)
		set[k] = struct{}{}
	}
	for _, inst := range instances {
		if _, ok := set[inst.ValueHash]; !ok {
			return false
		}
	}
	return true
}

// InstancesPage is one page of a challenge's pool.
type InstancesPage struct {
	Instances []db.AdminListInstancesRow
	Total     int64
}

// ListInstances pages through a challenge's pool, newest generation first, each row carrying who it
// was issued to.
func (s *Service) ListInstances(ctx context.Context, challengeID int64, page, perPage int) (InstancesPage, error) {
	rows, err := s.q.AdminListInstances(ctx, db.AdminListInstancesParams{
		ChallengeID: challengeID,
		Lim:         int32(perPage),              //nolint:gosec // Huma caps per_page
		Off:         int32((page - 1) * perPage), //nolint:gosec // page is bounded by the handler; an over-large offset just returns an empty page
	})
	if err != nil {
		return InstancesPage{}, fmt.Errorf("adminops: list instances for challenge %d: %w", challengeID, err)
	}
	p := InstancesPage{Instances: rows}
	if len(rows) > 0 {
		p.Total = rows[0].Total
	}
	return p, nil
}

// PoolStat is one challenge's pool utilisation.
type PoolStat struct {
	ChallengeID int64
	Name        string
	Total       int64
	Issued      int64
}

// PoolStats returns utilisation for every unique-flag challenge that has a pool — the pre-event gauge
// that makes exhaustion preventable rather than merely loud. A challenge with no instances yet does
// not appear (the JOIN drops it): an empty pool is a red bar the author has not built, not a zero
// this list needs to carry.
func (s *Service) PoolStats(ctx context.Context) ([]PoolStat, error) {
	rows, err := s.q.PoolStats(ctx)
	if err != nil {
		return nil, fmt.Errorf("adminops: pool stats: %w", err)
	}
	out := make([]PoolStat, len(rows))
	for i, r := range rows {
		out[i] = PoolStat{ChallengeID: r.ChallengeID, Name: r.Name, Total: r.Total, Issued: r.Issued}
	}
	return out, nil
}
