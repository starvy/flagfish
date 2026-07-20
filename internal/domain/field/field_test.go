package field_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/starvy/flagfish/internal/domain/field"
)

func TestCleanName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, in, want string
		wantErr        error
	}{
		{"trims", "  Affiliation  ", "Affiliation", nil},
		{"empty", "", "", field.ErrEmptyName},
		{"whitespace only", "  \t ", "", field.ErrEmptyName},
		{"at cap", strings.Repeat("x", field.MaxNameLen), strings.Repeat("x", field.MaxNameLen), nil},
		{"over cap", strings.Repeat("x", field.MaxNameLen+1), "", field.ErrNameTooLong},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := field.CleanName(tt.in)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("CleanName(%q) err = %v, want %v", tt.in, err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("CleanName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCleanDescription(t *testing.T) {
	t.Parallel()
	s := func(v string) *string { return &v }
	long := strings.Repeat("y", field.MaxDescriptionLen+1)
	tests := []struct {
		name    string
		in      *string
		want    *string
		wantErr error
	}{
		{"nil stays nil", nil, nil, nil},
		{"blank becomes nil", s("   "), nil, nil},
		{"trims", s("  help  "), s("help"), nil},
		{"over cap", s(long), nil, field.ErrDescriptionTooLong},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := field.CleanDescription(tt.in)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			switch {
			case got == nil && tt.want == nil:
			case got == nil || tt.want == nil:
				t.Fatalf("got %v, want %v", got, tt.want)
			case *got != *tt.want:
				t.Fatalf("got %q, want %q", *got, *tt.want)
			}
		})
	}
}

func TestValidTypeAndAppliesTo(t *testing.T) {
	t.Parallel()
	if !field.ValidType(field.TypeText) || !field.ValidType(field.TypeBoolean) {
		t.Fatal("known types must be valid")
	}
	if field.ValidType("dropdown") {
		t.Fatal("unknown type must be invalid")
	}
	if !field.ValidAppliesTo(field.AppliesUser) || !field.ValidAppliesTo(field.AppliesTeam) {
		t.Fatal("known applies_to must be valid")
	}
	if field.ValidAppliesTo("robot") {
		t.Fatal("unknown applies_to must be invalid")
	}
}

func TestIsAnswered(t *testing.T) {
	t.Parallel()
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	tests := []struct {
		name string
		in   json.RawMessage
		want bool
	}{
		{"nil", nil, false},
		{"empty bytes", raw(""), false},
		{"json null", raw("null"), false},
		{"empty string", raw(`""`), false},
		{"text", raw(`"MIT"`), true},
		{"bool true", raw("true"), true},
		{"bool false is answered", raw("false"), true},
		{"whitespace-padded null", raw("  null  "), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := field.IsAnswered(tt.in); got != tt.want {
				t.Fatalf("IsAnswered(%s) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestNormalize(t *testing.T) {
	t.Parallel()
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	tests := []struct {
		name        string
		fieldType   string
		in          json.RawMessage
		wantValue   string
		wantPresent bool
		wantErr     error
	}{
		// text
		{"text value", field.TypeText, raw(`"MIT"`), `"MIT"`, true, nil},
		{"text trims", field.TypeText, raw(`"  MIT  "`), `"MIT"`, true, nil},
		{"text empty string is absent", field.TypeText, raw(`""`), "null", false, nil},
		{"text whitespace is absent", field.TypeText, raw(`"   "`), "null", false, nil},
		{"text nil is absent", field.TypeText, nil, "null", false, nil},
		{"text json null is absent", field.TypeText, raw(`null`), "null", false, nil},
		{"text rejects a bool", field.TypeText, raw(`true`), "", false, field.ErrTypeMismatch},
		{"text rejects a number", field.TypeText, raw(`42`), "", false, field.ErrTypeMismatch},
		{"text over cap", field.TypeText, raw(`"` + strings.Repeat("z", field.MaxTextAnswerLen+1) + `"`), "", false, field.ErrTextTooLong},
		// boolean — both true and false are present, matching the login gate
		{"bool true", field.TypeBoolean, raw(`true`), "true", true, nil},
		{"bool false is present", field.TypeBoolean, raw(`false`), "false", true, nil},
		{"bool nil is absent", field.TypeBoolean, nil, "null", false, nil},
		{"bool json null is absent", field.TypeBoolean, raw(`null`), "null", false, nil},
		{"bool rejects a string", field.TypeBoolean, raw(`"yes"`), "", false, field.ErrTypeMismatch},
		// unknown type
		{"unknown type", "dropdown", raw(`"x"`), "", false, field.ErrUnknownType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotVal, gotPresent, err := field.Normalize(tt.fieldType, tt.in)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Normalize(%s, %s) err = %v, want %v", tt.fieldType, tt.in, err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if gotPresent != tt.wantPresent {
				t.Fatalf("Normalize present = %v, want %v", gotPresent, tt.wantPresent)
			}
			if string(gotVal) != tt.wantValue {
				t.Fatalf("Normalize value = %s, want %s", gotVal, tt.wantValue)
			}
		})
	}
}
