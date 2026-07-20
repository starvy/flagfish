package prereq

import (
	"reflect"
	"testing"
)

// TestEncodeRoundTrip pins Encode as the exact inverse of Parse: whatever the admin API writes,
// every reader of the column decodes back to the same value.
func TestEncodeRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   Requirements
		want string
	}{
		{name: "zero value is the column default", in: Requirements{}, want: `{}`},
		{
			name: "prereqs hidden omit anonymize",
			in:   Requirements{Prerequisites: []int64{1, 2}},
			want: `{"prerequisites":[1,2]}`,
		},
		{
			name: "masked keeps the imported bool spelling",
			in:   Requirements{Prerequisites: []int64{3}, Visibility: Masked},
			want: `{"anonymize":true,"prerequisites":[3]}`,
		},
		{
			name: "preview keeps the imported string spelling",
			in:   Requirements{Prerequisites: []int64{3}, Visibility: Preview},
			want: `{"anonymize":"preview","prerequisites":[3]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := Encode(tc.in)
			if err != nil {
				t.Fatalf("Encode(%+v): %v", tc.in, err)
			}
			if string(raw) != tc.want {
				t.Fatalf("Encode(%+v) = %s, want %s", tc.in, raw, tc.want)
			}
			back, err := Parse(raw)
			if err != nil {
				t.Fatalf("Parse(Encode(%+v)): %v", tc.in, err)
			}
			if !reflect.DeepEqual(back.Prerequisites, tc.in.Prerequisites) || back.Visibility != tc.in.Visibility {
				t.Fatalf("round trip = %+v, want %+v", back, tc.in)
			}
		})
	}
}

func TestVisibilityNames(t *testing.T) {
	for _, v := range []Visibility{Hidden, Masked, Preview} {
		back, err := VisibilityFromName(v.Name())
		if err != nil || back != v {
			t.Errorf("VisibilityFromName(%q) = %v, %v; want %v", v.Name(), back, err, v)
		}
	}
	if _, err := VisibilityFromName("nope"); err == nil {
		t.Error("VisibilityFromName accepted an unknown name")
	}
}

func TestFindCycle(t *testing.T) {
	tests := []struct {
		name  string
		edges map[int64][]int64
		start int64
		want  []int64
	}{
		{name: "no edges", edges: map[int64][]int64{}, start: 1, want: nil},
		{
			name:  "chain is no cycle",
			edges: map[int64][]int64{1: {2}, 2: {3}},
			start: 1,
			want:  nil,
		},
		{
			name:  "self loop",
			edges: map[int64][]int64{1: {1}},
			start: 1,
			want:  []int64{1, 1},
		},
		{
			name:  "two-node cycle",
			edges: map[int64][]int64{1: {2}, 2: {1}},
			start: 1,
			want:  []int64{1, 2, 1},
		},
		{
			name:  "longer cycle behind a dead end",
			edges: map[int64][]int64{1: {9, 2}, 2: {3}, 3: {1}},
			start: 1,
			want:  []int64{1, 2, 3, 1},
		},
		{
			name:  "cycle elsewhere does not implicate start",
			edges: map[int64][]int64{1: {2}, 2: {3}, 3: {2}},
			start: 1,
			want:  nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := FindCycle(tc.edges, tc.start); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("FindCycle(%v, %d) = %v, want %v", tc.edges, tc.start, got, tc.want)
			}
		})
	}
}
