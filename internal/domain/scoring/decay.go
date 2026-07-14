// Package scoring holds the pure scoring rules: the decay curve that sets a
// challenge's current asking price, and the ordering rule that turns scoring
// events into a standings board.
//
// This package imports nothing outside the standard library. It knows nothing
// about SQL, HTTP or River, and it never mutates its inputs.
package scoring

import (
	"errors"
	"fmt"
)

// Function is how a challenge's value responds to being solved.
type Function uint8

const (
	// FunctionStatic never recomputes: the stored value is the value.
	FunctionStatic Function = iota
	// FunctionLinear subtracts Decay points per solve beyond the first.
	FunctionLinear
	// FunctionLogarithmic is a downward parabola reaching Minimum at exactly
	// Decay solves beyond the first.
	FunctionLogarithmic
)

var functionNames = map[Function]string{
	FunctionStatic:      "static",
	FunctionLinear:      "linear",
	FunctionLogarithmic: "logarithmic",
}

func (f Function) String() string {
	if s, ok := functionNames[f]; ok {
		return s
	}
	return fmt.Sprintf("Function(%d)", uint8(f))
}

// ErrUnknownFunction is returned by ParseFunction for an unrecognised name.
// An unknown curve name is a hard parse error; a silent fallback would let a
// typo quietly change a challenge's scoring model.
var ErrUnknownFunction = errors.New("scoring: unknown decay function")

// ParseFunction maps a stored function name onto a Function.
func ParseFunction(s string) (Function, error) {
	for f, name := range functionNames {
		if name == s {
			return f, nil
		}
	}
	return 0, fmt.Errorf("%w: %q", ErrUnknownFunction, s)
}

// Decays reports whether the value is recomputed on each solve.
func (f Function) Decays() bool { return f != FunctionStatic }

// A Curve is a challenge's value model: the four columns
// (function, initial, minimum, decay) that decide what the challenge is worth
// after N solves.
type Curve struct {
	Function Function
	Initial  int // value at the first solve
	Minimum  int // floor; the value never drops below it
	Decay    int // linear: points lost per solve. logarithmic: solves-beyond-the-first to reach Minimum.
}

// ErrInvalidCurve is returned by Curve.Validate.
var ErrInvalidCurve = errors.New("scoring: invalid curve")

// Validate rejects a curve that cannot produce a sane value. It is called at
// challenge create/update time and at import: a malformed curve must fail at the
// boundary, not silently produce nonsense three months into an event.
// Decay >= 1 is required for a decaying function; see ValueAt for what a zero
// decay does if one reaches us anyway (import).
func (c Curve) Validate() error {
	if _, ok := functionNames[c.Function]; !ok {
		return fmt.Errorf("%w: %s", ErrInvalidCurve, c.Function)
	}
	if c.Initial < 0 {
		return fmt.Errorf("%w: initial (%d) must be >= 0", ErrInvalidCurve, c.Initial)
	}
	if !c.Function.Decays() {
		return nil
	}
	if c.Minimum < 0 {
		return fmt.Errorf("%w: minimum (%d) must be >= 0", ErrInvalidCurve, c.Minimum)
	}
	if c.Minimum > c.Initial {
		return fmt.Errorf("%w: minimum (%d) must be <= initial (%d)", ErrInvalidCurve, c.Minimum, c.Initial)
	}
	if c.Decay < 1 {
		return fmt.Errorf("%w: decay (%d) must be >= 1 for a %s curve", ErrInvalidCurve, c.Decay, c.Function)
	}
	return nil
}

// ValueAt returns the challenge's value once `solves` accounts have solved it.
//
// `solves` is the effective solve count: solves by hidden or banned accounts are
// excluded by the caller's query. The adjusted count fed to the formula is
// n = max(solves-1, 0), so the first solver pays full Initial — the decrement
// lives here and must not be applied again by a caller.
//
// ValueAt is pure: it never mutates the Curve.
//
// Zero decay: a validated curve cannot have Decay < 1, but an imported archive
// can carry decay = 0 rows. For a logarithmic curve we then return Minimum,
// which is exactly what the SQL recompute yields — Postgres and Go agree, and
// neither rewrites the author's decay column behind their back.
func (c Curve) ValueAt(solves int) int {
	if !c.Function.Decays() {
		return c.Initial // never recomputed
	}

	n := solves - 1
	if n < 0 {
		n = 0
	}

	var v int
	switch c.Function {
	case FunctionLinear:
		// ceil(initial - decay*n); integer arithmetic is already exact.
		v = c.Initial - c.Decay*n

	case FunctionLogarithmic:
		if c.Decay <= 0 {
			return c.Minimum // see the "Zero decay" note above
		}
		// Past the knee the curve is flat. Returning early is not just an
		// optimisation: it bounds n, which is what keeps the product below from
		// overflowing.
		if n >= c.Decay {
			return c.Minimum
		}

		// Integer arithmetic, deliberately — do not "simplify" to floats. In float64
		// the curve is wrong at the boundary: for (initial=3901, minimum=205,
		// decay=142) the true value at n == decay is 205, but the float lands a hair
		// high and Ceil lifts it to 206, so the challenge never reaches its floor.
		// The same curve also runs in SQL (RecalcChallengeValue), and Go and Postgres
		// need not round a float identically — displayed and stored price would
		// silently diverge. Over the integers, init - floor(D*n^2/decay^2) with
		// D = initial - minimum, both engines do the same exact truncating division.
		//
		// n < decay here, so D*n*n cannot overflow int64 for any curve Validate admits.
		d := int64(c.Decay)
		drop := (int64(c.Initial-c.Minimum) * int64(n) * int64(n)) / (d * d)
		v = c.Initial - int(drop)

	default:
		return c.Initial
	}

	if v < c.Minimum {
		return c.Minimum
	}
	return v
}
