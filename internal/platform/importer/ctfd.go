package importer

import (
	"encoding/json"
	"fmt"
)

// The archive's on-disk shapes. Each db/<table>.json is one object: {"count", "results":[rows],
// "meta":{}}. Rows are raw database rows with exact column names and ids preserved. We model only the
// columns we translate; unknown columns are ignored per-row, but an unknown *table* or an unknown
// discriminator value is surfaced, never silently skipped.

// tableEnvelope is the wrapper every table dump shares.
type tableEnvelope struct {
	Count   int               `json:"count"`
	Results []json.RawMessage `json:"results"`
	Meta    json.RawMessage   `json:"meta"`
}

// alembicRow is the single row in db/alembic_version.json — the archive's only real version marker.
type alembicRow struct {
	VersionNum string `json:"version_num"`
}

// revisionOrdinal maps a known schema revision onto a linear position. The revision graph is a single
// chain, so an ordinal is enough to gate the handful of places where the *meaning* of a column
// changed and column presence alone cannot reveal it.
var revisionOrdinal = map[string]int{
	"8369118943a1": 0, "4e4d5a9ea000": 1, "b5551cd26764": 2, "b295b033364d": 3,
	"080d29b15cd3": 4, "a03403986a32": 5, "1093835a1051": 6, "0366ba6575ca": 7,
	"75e8ab9a0014": 8, "07dfbe5e1edc": 9, "ef87d69ec29a": 10, "6012fe8de495": 11,
	"4d3c1b59d011": 12, "46a278193a94": 13, "0def790057c1": 14, "9e6f6578ca84": 15,
	"5c4996aeb2cb": 16, "9889b8c53673": 17, "a02c5bf43407": 18, "4fe3eeed9a9d": 19,
	"a49ad66aa0f1": 20, "62bf576b2cd3": 21, "f73a96c97449": 22, "662d728ad7da": 23,
	"364b4efa1686": 24, "5c98d9253f56": 25, "55623b100da8": 26, "24ad6790bc3c": 27,
	"67ebab6de598": 28, "48d8250d19bd": 29, "336b8c601b94": 30,
}

// rejectedRevisions are the 1.x revisions whose schema predates the 2.x model entirely — the same
// set the source platform itself refuses to restore.
var rejectedRevisions = map[string]bool{
	"1ec4a28fe0ff": true, "2539d8b5082e": true, "7e9efd084c5a": true, "87733981ca0e": true,
	"a4e30c94c360": true, "c12d2a1b0926": true, "c7225db614c1": true, "cb3cfcc47e2f": true,
	"cbf5620f8e15": true, "d5a224bf5862": true, "d6514ec92738": true, "dab615389702": true,
	"e62fd69bd417": true,
}

// admit resolves a revision to its ordinal, or explains why the archive is refused. assumeRevision,
// when set, translates an otherwise-unknown revision as that known shape — loud, opt-in, unsupported.
func admit(revision, assumeRevision string) (int, error) {
	if revision == "" {
		return 0, fmt.Errorf("archive has no schema revision: db/alembic_version.json is missing or empty")
	}
	if rejectedRevisions[revision] {
		return 0, fmt.Errorf("archive schema revision %q predates the supported model and cannot be imported", revision)
	}
	if ord, ok := revisionOrdinal[revision]; ok {
		return ord, nil
	}
	if assumeRevision != "" {
		ord, ok := revisionOrdinal[assumeRevision]
		if !ok {
			return 0, fmt.Errorf("assume-revision %q is not a known revision", assumeRevision)
		}
		return ord, nil
	}
	return 0, fmt.Errorf("unknown schema revision %q: refusing to guess its semantics "+
		"(pass an explicit assume-revision to translate it as a known shape)", revision)
}

// ---- row shapes ----

type ctfdChallenge struct {
	ID             int64           `json:"id"`
	Name           string          `json:"name"`
	Category       string          `json:"category"`
	Description    string          `json:"description"`
	Attribution    *string         `json:"attribution"`
	ConnectionInfo *string         `json:"connection_info"`
	Value          *int32          `json:"value"`
	Type           string          `json:"type"`
	State          string          `json:"state"`
	MaxAttempts    *int32          `json:"max_attempts"`
	NextID         *int64          `json:"next_id"`
	Requirements   json.RawMessage `json:"requirements"`
	// Present only at/after the ordinal that moves dynamic params onto challenges. Consulted via the
	// merge with dynamic_challenge either way.
	Initial  *int32  `json:"initial"`
	Minimum  *int32  `json:"minimum"`
	Decay    *int32  `json:"decay"`
	Function *string `json:"function"`
}

// ctfdDynamicChallenge is the joined-table row that holds dynamic scoring for type='dynamic'
// challenges. Its columns win over the same-named columns on the challenge row.
type ctfdDynamicChallenge struct {
	ID       int64   `json:"id"`
	Value    *int32  `json:"value"`
	Initial  *int32  `json:"initial"`
	Minimum  *int32  `json:"minimum"`
	Decay    *int32  `json:"decay"`
	Function *string `json:"function"`
}

type ctfdFlag struct {
	ID          int64  `json:"id"`
	ChallengeID int64  `json:"challenge_id"`
	Type        string `json:"type"`
	Content     string `json:"content"`
	Data        string `json:"data"`
}

type ctfdTag struct {
	ID          int64  `json:"id"`
	ChallengeID int64  `json:"challenge_id"`
	Value       string `json:"value"`
}

type ctfdHint struct {
	ID           int64           `json:"id"`
	ChallengeID  int64           `json:"challenge_id"`
	Title        *string         `json:"title"`
	Content      string          `json:"content"`
	Cost         int32           `json:"cost"`
	Requirements json.RawMessage `json:"requirements"`
}

type ctfdFile struct {
	ID          int64   `json:"id"`
	Type        string  `json:"type"`
	Location    string  `json:"location"`
	ChallengeID *int64  `json:"challenge_id"`
	Sha1sum     *string `json:"sha1sum"`
}

type ctfdBracket struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Type        string  `json:"type"`
}

type ctfdUser struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Email       string  `json:"email"`
	Password    *string `json:"password"`
	Type        string  `json:"type"`
	Secret      *string `json:"secret"`
	Website     *string `json:"website"`
	Affiliation *string `json:"affiliation"`
	Country     *string `json:"country"`
	Language    *string `json:"language"`
	BracketID   *int64  `json:"bracket_id"`
	TeamID      *int64  `json:"team_id"`
	Hidden      bool    `json:"hidden"`
	Banned      bool    `json:"banned"`
	Verified    bool    `json:"verified"`
}

type ctfdTeam struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Email       *string `json:"email"`
	Password    *string `json:"password"`
	Secret      *string `json:"secret"`
	Website     *string `json:"website"`
	Affiliation *string `json:"affiliation"`
	Country     *string `json:"country"`
	BracketID   *int64  `json:"bracket_id"`
	CaptainID   *int64  `json:"captain_id"`
	Hidden      bool    `json:"hidden"`
	Banned      bool    `json:"banned"`
}

type ctfdConfig struct {
	ID    int64   `json:"id"`
	Key   string  `json:"key"`
	Value *string `json:"value"`
}

type ctfdSubmission struct {
	ID          int64   `json:"id"`
	ChallengeID int64   `json:"challenge_id"`
	UserID      *int64  `json:"user_id"`
	TeamID      *int64  `json:"team_id"`
	Type        string  `json:"type"`
	Provided    string  `json:"provided"`
	IP          *string `json:"ip"`
	Date        string  `json:"date"`
}

type ctfdSolve struct {
	ID          int64  `json:"id"`
	ChallengeID int64  `json:"challenge_id"`
	UserID      *int64 `json:"user_id"`
	TeamID      *int64 `json:"team_id"`
}

type ctfdAward struct {
	ID          int64   `json:"id"`
	UserID      *int64  `json:"user_id"`
	TeamID      *int64  `json:"team_id"`
	Type        string  `json:"type"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Value       int32   `json:"value"`
	Category    *string `json:"category"`
	Icon        *string `json:"icon"`
	Date        string  `json:"date"`
}
