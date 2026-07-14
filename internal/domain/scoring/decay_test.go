package scoring_test

import (
	"errors"
	"math"
	"math/rand"
	"testing"

	"github.com/starvy/flagfish/internal/domain/scoring"
)

func TestParseFunction(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    scoring.Function
		wantErr bool
	}{
		{in: "static", want: scoring.FunctionStatic},
		{in: "linear", want: scoring.FunctionLinear},
		{in: "logarithmic", want: scoring.FunctionLogarithmic},
		{in: "", wantErr: true},
		{in: "STATIC", wantErr: true},
		{in: "logarithimc", wantErr: true}, // a typo must be an error, never silently a curve
	} {
		got, err := scoring.ParseFunction(tc.in)
		if tc.wantErr {
			if !errors.Is(err, scoring.ErrUnknownFunction) {
				t.Errorf("ParseFunction(%q): want ErrUnknownFunction, got %v", tc.in, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("ParseFunction(%q) = %v, %v; want %v", tc.in, got, err, tc.want)
		}
	}
}

func TestCurveValidate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		curve scoring.Curve
		ok    bool
	}{
		{"static ignores the decay columns", scoring.Curve{Function: scoring.FunctionStatic, Initial: 500}, true},
		{"sane logarithmic", scoring.Curve{Function: scoring.FunctionLogarithmic, Initial: 500, Minimum: 100, Decay: 20}, true},
		{"sane linear", scoring.Curve{Function: scoring.FunctionLinear, Initial: 100, Minimum: 10, Decay: 5}, true},
		{"decay 0 is REJECTED, not coerced to 1", scoring.Curve{Function: scoring.FunctionLogarithmic, Initial: 500, Minimum: 100, Decay: 0}, false},
		{"negative decay", scoring.Curve{Function: scoring.FunctionLinear, Initial: 500, Minimum: 100, Decay: -1}, false},
		{"minimum above initial", scoring.Curve{Function: scoring.FunctionLogarithmic, Initial: 100, Minimum: 500, Decay: 20}, false},
		{"negative initial", scoring.Curve{Function: scoring.FunctionLinear, Initial: -1, Minimum: 0, Decay: 1}, false},
		{"unknown function", scoring.Curve{Function: scoring.Function(99), Initial: 1}, false},
	} {
		err := tc.curve.Validate()
		if tc.ok && err != nil {
			t.Errorf("%s: want valid, got %v", tc.name, err)
		}
		if !tc.ok {
			if err == nil {
				t.Errorf("%s: want invalid, got nil", tc.name)
			} else if !errors.Is(err, scoring.ErrInvalidCurve) {
				t.Errorf("%s: want ErrInvalidCurve, got %v", tc.name, err)
			}
		}
	}
}

// These numbers are the pinned oracle for the scoring model: change the code to
// fit them, never the other way around.
func TestValueAtGoldenNumbers(t *testing.T) {
	linear := scoring.Curve{Function: scoring.FunctionLinear, Initial: 100, Minimum: 10, Decay: 10}
	for _, tc := range []struct{ solves, want int }{
		{0, 100}, // no solves yet: back to initial
		{1, 100}, // first solver pays full price (n = 0)
		{2, 90},
		{3, 80},
		{4, 70},
		{10, 10},
		{99, 10}, // clamped at minimum
	} {
		if got := linear.ValueAt(tc.solves); got != tc.want {
			t.Errorf("linear.ValueAt(%d) = %d, want %d", tc.solves, got, tc.want)
		}
	}

	log := scoring.Curve{Function: scoring.FunctionLogarithmic, Initial: 500, Minimum: 100, Decay: 20}
	for _, tc := range []struct{ solves, want int }{
		{0, 500},
		{1, 500},  // n = 0 -> initial
		{21, 100}, // n = decay -> exactly minimum
		{50, 100}, // clamped
	} {
		if got := log.ValueAt(tc.solves); got != tc.want {
			t.Errorf("log.ValueAt(%d) = %d, want %d", tc.solves, got, tc.want)
		}
	}

	static := scoring.Curve{Function: scoring.FunctionStatic, Initial: 350, Minimum: 1, Decay: 1}
	for _, solves := range []int{0, 1, 500} {
		if got := static.ValueAt(solves); got != 350 {
			t.Errorf("static.ValueAt(%d) = %d, want 350 (never recomputed)", solves, got)
		}
	}
}

// Two halves of the same hazard:
//  1. ValueAt must not mutate the curve — a compute path that "fixes" decay = 0
//     by writing 1 back would permanently rewrite the author's data.
//  2. The value it produces for decay == 0 must agree with the SQL recompute,
//     which yields GREATEST(minimum, NULL) = minimum via NULLIF(decay, 0).
func TestZeroDecayIsNeitherPersistedNorGuessed(t *testing.T) {
	c := scoring.Curve{Function: scoring.FunctionLogarithmic, Initial: 500, Minimum: 100, Decay: 0}

	if got := c.ValueAt(5); got != 100 {
		t.Errorf("ValueAt with decay=0 = %d, want minimum (100) — matching NULLIF(decay,0) in SQL", got)
	}
	if c.Decay != 0 {
		t.Fatalf("ValueAt MUTATED the curve: decay is now %d; ValueAt must be pure", c.Decay)
	}
	if err := c.Validate(); err == nil {
		t.Error("a decay=0 logarithmic curve must fail Validate() at the write boundary")
	}
}

// ---------------------------------------------------------------------------
// Property tests. A valid curve must satisfy these for every solve count.
// ---------------------------------------------------------------------------

func randomValidCurve(r *rand.Rand) scoring.Curve {
	fn := []scoring.Function{scoring.FunctionLinear, scoring.FunctionLogarithmic}[r.Intn(2)]
	initial := r.Intn(5000) + 1
	minimum := r.Intn(initial + 1)
	return scoring.Curve{Function: fn, Initial: initial, Minimum: minimum, Decay: r.Intn(200) + 1}
}

func TestPropertyValueIsBoundedAndMonotonic(t *testing.T) {
	r := rand.New(rand.NewSource(0xC7FD))
	for range 2000 {
		c := randomValidCurve(r)
		if err := c.Validate(); err != nil {
			t.Fatalf("generator produced an invalid curve %+v: %v", c, err)
		}

		prev := math.MaxInt
		for solves := range 401 {
			v := c.ValueAt(solves)

			// Bounded: minimum <= v <= initial. Never negative, never above the
			// asking price the author set.
			if v < c.Minimum || v > c.Initial {
				t.Fatalf("%+v ValueAt(%d) = %d, out of [%d, %d]", c, solves, v, c.Minimum, c.Initial)
			}
			// Monotonically non-increasing: solving a challenge can never make it
			// worth more. A curve that rebounds is a scoring exploit.
			if v > prev {
				t.Fatalf("%+v ValueAt(%d) = %d rose above ValueAt(%d) = %d", c, solves, v, solves-1, prev)
			}
			prev = v
		}

		// The first solver always pays full price.
		if got := c.ValueAt(1); got != c.Initial {
			t.Fatalf("%+v: first solve worth %d, want initial %d", c, got, c.Initial)
		}
		// And an unsolved challenge sits at initial too (n = 0, no decrement).
		if got := c.ValueAt(0); got != c.Initial {
			t.Fatalf("%+v: unsolved worth %d, want initial %d", c, got, c.Initial)
		}
	}
}

// The logarithmic curve's contract with the author: "decay" is the number of
// solves beyond the first at which the challenge bottoms out. At n == decay the
// formula must land on minimum exactly — not one point above, not clamped there
// from below.
func TestPropertyLogarithmicHitsMinimumExactlyAtDecay(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for range 500 {
		c := randomValidCurve(r)
		c.Function = scoring.FunctionLogarithmic

		if got := c.ValueAt(c.Decay + 1); got != c.Minimum { // solves = n+1
			t.Fatalf("%+v: at n=decay value is %d, want minimum %d", c, got, c.Minimum)
		}
		if c.Decay > 1 && c.Minimum < c.Initial {
			// One solve earlier it must still be strictly above the floor,
			// otherwise the curve is bottoming out early and the author's
			// "decay" knob is lying to them.
			if got := c.ValueAt(c.Decay); got < c.Minimum {
				t.Fatalf("%+v: value below minimum before n=decay: %d", c, got)
			}
		}
	}
}

func TestPropertyValueAtIsPure(t *testing.T) {
	r := rand.New(rand.NewSource(99))
	for range 500 {
		c := randomValidCurve(r)
		before := c
		for solves := range 50 {
			_ = c.ValueAt(solves)
		}
		if c != before {
			t.Fatalf("ValueAt mutated the curve: %+v -> %+v", before, c)
		}
	}
}
