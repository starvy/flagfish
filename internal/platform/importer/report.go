package importer

import "time"

// Severity ranks a finding. An Error finding means the archive could not be faithfully translated;
// the loader refuses to commit when the report carries one, so a lossy-but-committed import and a
// rejected one are never confused.
type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// Finding codes. Stable strings so the golden report is diff-able and an admin UI can branch on them
// without parsing prose.
const (
	CodeDeferredTable   = "DEFERRED_TABLE"   // a subsystem we do not model yet: counted, named, dropped
	CodeUnmappedTable   = "UNMAPPED_TABLE"   // a table with no counterpart at all (plugin data)
	CodeTokensDropped   = "TOKENS_DROPPED"   // plaintext credentials are never imported
	CodeConfigDuplicate = "CONFIG_DUPLICATE" // duplicate config key resolved last-writer-wins
	CodeConfigPreserved = "CONFIG_PRESERVED" // an unmodelled config key kept verbatim via the raw seam
	CodeSolveValueStamp = "SOLVE_VALUE_STAMPED"
	CodeFileNoBlob      = "FILE_WITHOUT_BLOB" // a file row whose upload is absent from the archive
	CodeBlobNoFile      = "BLOB_WITHOUT_FILE" // an upload with no owning file row
	CodeUnknownFlagType = "UNKNOWN_FLAG_TYPE" // outside {static, regex}; unsolvable if dropped -> hard fail
	CodeUnknownChalType = "UNKNOWN_CHALLENGE_TYPE"
	CodeCorruptSolve    = "CORRUPT_SOLVE" // a solve with no matching correct submission, or vice versa
	CodeRequirementsFix = "REQUIREMENTS_NORMALIZED"
)

// A Finding is one structured line in the report. Nothing is dropped silently; everything the
// importer decides not to carry across shows up here, named and counted.
type Finding struct {
	Severity Severity `json:"severity"`
	Code     string   `json:"code"`
	Table    string   `json:"table,omitempty"`
	Count    int      `json:"count,omitempty"`
	Detail   string   `json:"detail,omitempty"`
}

// TableStat is the row-count triple that makes "no silent loss" checkable by arithmetic:
// Written == Read - Dropped for every mapped table.
type TableStat struct {
	Read    int `json:"read"`
	Written int `json:"written"`
	Dropped int `json:"dropped"`
}

// Report is the first-class artifact of an import. It is serialised (and golden-filed in tests) so a
// change that starts dropping rows shows up as a diff, not as a surprise mid-event.
type Report struct {
	SourceRevision string               `json:"source_revision"`
	SourceOrdinal  int                  `json:"source_ordinal"`
	SourceHint     string               `json:"source_hint,omitempty"` // config.ctf_version — advisory only
	Rows           map[string]TableStat `json:"rows"`
	Findings       []Finding            `json:"findings"`
	StartedAt      time.Time            `json:"started_at,omitempty"`
	EndedAt        time.Time            `json:"ended_at,omitempty"`
}

func newReport() *Report {
	return &Report{Rows: map[string]TableStat{}, Findings: []Finding{}}
}

func (r *Report) note(sev Severity, code, table string, count int, detail string) {
	r.Findings = append(r.Findings, Finding{Severity: sev, Code: code, Table: table, Count: count, Detail: detail})
}

// hasError reports whether any finding blocks the commit.
func (r *Report) hasError() bool {
	for _, f := range r.Findings {
		if f.Severity == SeverityError {
			return true
		}
	}
	return false
}

func (r *Report) read(table string, n int) {
	s := r.Rows[table]
	s.Read += n
	r.Rows[table] = s
}

func (r *Report) wrote(table string, n int) {
	s := r.Rows[table]
	s.Written += n
	r.Rows[table] = s
}

func (r *Report) dropped(table string, n int) {
	s := r.Rows[table]
	s.Dropped += n
	r.Rows[table] = s
}
