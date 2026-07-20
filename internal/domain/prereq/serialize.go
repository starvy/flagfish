package prereq

import (
	"encoding/json"
	"fmt"
)

// Names for the visibility vocabulary as the admin API speaks it. The stored form stays the
// imported `anonymize` spelling (bool | "preview"); these names never touch the column.
const (
	NameHidden  = "hidden"
	NameMasked  = "masked"
	NamePreview = "preview"
)

// Name returns the admin-API name of a visibility.
func (v Visibility) Name() string {
	switch v {
	case Masked:
		return NameMasked
	case Preview:
		return NamePreview
	default:
		return NameHidden
	}
}

// VisibilityFromName is the inverse of Name.
func VisibilityFromName(name string) (Visibility, error) {
	switch name {
	case NameHidden:
		return Hidden, nil
	case NameMasked:
		return Masked, nil
	case NamePreview:
		return Preview, nil
	}
	return Hidden, fmt.Errorf("prereq: unknown visibility %q", name)
}

// Encode is the inverse of Parse: it renders Requirements as the `requirements` jsonb document.
// No prerequisites and Hidden (the zero value) encode as {}, matching the column default, and the
// anonymize flag keeps its imported spelling so native writes and archive rows stay one format.
func Encode(r Requirements) ([]byte, error) {
	doc := map[string]any{}
	if len(r.Prerequisites) > 0 {
		doc["prerequisites"] = r.Prerequisites
	}
	switch r.Visibility {
	case Hidden:
	case Masked:
		doc["anonymize"] = true
	case Preview:
		doc["anonymize"] = "preview"
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("prereq: encode requirements: %w", err)
	}
	return out, nil
}

// FindCycle reports a prerequisite cycle through start: a path start → … → start over the given
// edges, or nil when start is on no cycle. Cycles are tolerated at runtime — the membership test
// never traverses — but a challenge on one can only be unlocked by an admin edit, so writes warn.
func FindCycle(edges map[int64][]int64, start int64) []int64 {
	visited := map[int64]bool{}
	var walk func(from int64, path []int64) []int64
	walk = func(from int64, path []int64) []int64 {
		// A fresh slice per step: siblings in the loop below must not share a backing array.
		path = append(append(make([]int64, 0, len(path)+1), path...), from)
		for _, next := range edges[from] {
			if next == start {
				return append(path, start)
			}
			if visited[next] {
				continue
			}
			visited[next] = true
			if found := walk(next, path); found != nil {
				return found
			}
		}
		return nil
	}
	return walk(start, nil)
}
