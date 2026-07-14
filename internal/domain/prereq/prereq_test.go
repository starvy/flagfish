package prereq

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    Requirements
		wantErr bool
	}{
		{name: "empty", raw: "", want: Requirements{}},
		{name: "default object", raw: `{}`, want: Requirements{}},
		{
			name: "prereqs no anonymize hide",
			raw:  `{"prerequisites":[1,2]}`,
			want: Requirements{Prerequisites: []int64{1, 2}, Visibility: Hidden},
		},
		{
			name: "anonymize false is hidden",
			raw:  `{"prerequisites":[3],"anonymize":false}`,
			want: Requirements{Prerequisites: []int64{3}, Visibility: Hidden},
		},
		{
			name: "anonymize true is masked",
			raw:  `{"prerequisites":[3],"anonymize":true}`,
			want: Requirements{Prerequisites: []int64{3}, Visibility: Masked},
		},
		{
			name: "anonymize preview",
			raw:  `{"prerequisites":[3],"anonymize":"preview"}`,
			want: Requirements{Prerequisites: []int64{3}, Visibility: Preview},
		},
		{
			name: "anonymize null is hidden",
			raw:  `{"prerequisites":[3],"anonymize":null}`,
			want: Requirements{Prerequisites: []int64{3}, Visibility: Hidden},
		},
		{name: "unknown anonymize string errors", raw: `{"anonymize":"nope"}`, wantErr: true},
		{name: "malformed json errors", raw: `{`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse([]byte(tc.raw))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Parse(%q): want error, got %+v", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q): unexpected error: %v", tc.raw, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Parse(%q) = %+v, want %+v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestVisibilityVisible(t *testing.T) {
	if Hidden.Visible() {
		t.Fatal("Hidden must not be visible")
	}
	if !Masked.Visible() || !Preview.Visible() {
		t.Fatal("Masked and Preview must be visible")
	}
}
