package importer

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPeelRequirements(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", ``, `{}`},
		{"null", `null`, `{}`},
		{"empty json string", `""`, `{}`},
		{"object passes through", `{"prerequisites":[1,2]}`, `{"prerequisites":[1,2]}`},
		{"string-encoded object", `"{\"prerequisites\": [3]}"`, `{"prerequisites": [3]}`},
		{"bare list wrapped", `[4,5]`, `{"prerequisites":[4,5]}`},
		{"string-encoded list wrapped", `"[6]"`, `{"prerequisites":[6]}`},
		{"garbage drops to empty", `"not json"`, `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := peelRequirements(json.RawMessage(tc.in), newReport())
			if strings.TrimSpace(string(got)) != tc.want {
				t.Fatalf("peel(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if !json.Valid(got) {
				t.Fatalf("peel(%q) produced invalid JSON %q", tc.in, got)
			}
		})
	}
}

func TestAdmit(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		rev     string
		assume  string
		wantOrd int
		wantErr bool
	}{
		{"known current", "48d8250d19bd", "", 29, false},
		{"known dynamic split", "67ebab6de598", "", 28, false},
		{"rejected 1.x", "e62fd69bd417", "", 0, true},
		{"unknown rejected", "ffffffffffff", "", 0, true},
		{"unknown assumed", "ffffffffffff", "48d8250d19bd", 29, false},
		{"empty", "", "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ord, err := admit(tc.rev, tc.assume)
			if tc.wantErr != (err != nil) {
				t.Fatalf("admit(%q,%q) err=%v, wantErr=%v", tc.rev, tc.assume, err, tc.wantErr)
			}
			if err == nil && ord != tc.wantOrd {
				t.Fatalf("admit(%q) ord=%d, want %d", tc.rev, ord, tc.wantOrd)
			}
		})
	}
}

func TestUnsafePath(t *testing.T) {
	t.Parallel()
	unsafe := []string{"/etc/passwd", "../x", "a/../../b", "db/..", "a//b", "c\\d", "\\abs"}
	for _, p := range unsafe {
		if !unsafePath(p) {
			t.Errorf("unsafePath(%q) = false, want true", p)
		}
	}
	safe := []string{"db/challenges.json", "uploads/abc/flag.txt", "db/alembic_version.json"}
	for _, p := range safe {
		if unsafePath(p) {
			t.Errorf("unsafePath(%q) = true, want false", p)
		}
	}
}

// TestTranslateUnknownFlagTypeFails proves an unsolvable flag fails the whole import rather than
// being silently dropped.
func TestTranslateUnknownFlagTypeFails(t *testing.T) {
	t.Parallel()
	a := &Archive{Revision: "48d8250d19bd", Ordinal: 29, tables: map[string]*tableEnvelope{
		"config":     env(),
		"challenges": env(mustJSON(t, ctfdChallenge{ID: 1, Name: "c", Category: "misc", Type: "standard", Value: p32(100), State: "visible"})),
		"flags":      env(mustJSON(t, map[string]any{"id": 1, "challenge_id": 1, "type": "hashy", "content": "x"})),
	}}
	_, err := Translate(a, Options{}, newReport())
	if err == nil || !strings.Contains(err.Error(), "unsupported type") {
		t.Fatalf("want unsupported flag type error, got %v", err)
	}
}

// TestTranslateDynamicMerge proves dynamic_challenge wins over the challenge row's own columns.
func TestTranslateDynamicMerge(t *testing.T) {
	t.Parallel()
	a := &Archive{Revision: "48d8250d19bd", Ordinal: 29, tables: map[string]*tableEnvelope{
		"config": env(),
		"challenges": env(mustJSON(t, ctfdChallenge{
			ID: 7, Name: "dyn", Category: "pwn", Type: "dynamic", State: "visible",
			Value: p32(238), Initial: p32(999), Minimum: p32(999), Decay: p32(999),
		})),
		"dynamic_challenge": env(mustJSON(t, ctfdDynamicChallenge{
			ID: 7, Initial: p32(500), Minimum: p32(100), Decay: p32(20),
			Function: ps("linear"), Value: p32(238),
		})),
	}}
	plan, err := Translate(a, Options{}, newReport())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Challenges) != 1 {
		t.Fatalf("want 1 challenge, got %d", len(plan.Challenges))
	}
	c := plan.Challenges[0]
	if c.Function != "linear" || *c.Initial != 500 || *c.Minimum != 100 || *c.Decay != 20 {
		t.Fatalf("dynamic merge wrong: fn=%s initial=%v minimum=%v decay=%v", c.Function, c.Initial, c.Minimum, c.Decay)
	}
	if c.Value != 238 {
		t.Fatalf("challenge value = %d, want 238 (current asking price)", c.Value)
	}
}

func TestTranslateUnknownChallengeType(t *testing.T) {
	t.Parallel()
	mk := func() *Archive {
		return &Archive{Revision: "48d8250d19bd", Ordinal: 29, tables: map[string]*tableEnvelope{
			"config":     env(),
			"challenges": env(mustJSON(t, ctfdChallenge{ID: 1, Name: "c", Category: "misc", Type: "code", Value: p32(50), State: "visible"})),
		}}
	}
	if _, err := Translate(mk(), Options{}, newReport()); err == nil {
		t.Fatal("want hard failure on unknown challenge type")
	}
	plan, err := Translate(mk(), Options{ForceUnknownChallengeType: "standard"}, newReport())
	if err != nil {
		t.Fatalf("force override should succeed: %v", err)
	}
	if plan.Challenges[0].Type != "standard" {
		t.Fatalf("forced type = %q, want standard", plan.Challenges[0].Type)
	}
}

// ---- tiny helpers for the pure tests ----

func env(rows ...json.RawMessage) *tableEnvelope {
	if rows == nil {
		rows = []json.RawMessage{}
	}
	return &tableEnvelope{Count: len(rows), Results: rows}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func p32(v int32) *int32  { return &v }
func ps(s string) *string { return &s }
