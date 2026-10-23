package engine

import (
	"math"
	"strconv"
	"strings"
)

// The Ruby behaviours Fuse's pricing leans on, reproduced so the Go port
// answers the same figures to the cent. Each is checked against values
// recorded from Ruby 4.0.1 (testdata/austin/ruby_oracles.json.gz).

// rubyRound is Ruby's Float#round(ndigits) for ndigits > 0: MRI's
// rb_float_round (float.c) — its overflow and underflow guards, then
// round_half_up(x, 10**ndigits) / 10**ndigits. Ruby rounds miles
// ((m / 1609.344).round(2)), hours (round(4)) and labels (round(1)) this way,
// and it isn't the same as formatting to n places: round_half_up nudges a
// product that landed just under a half back up.
func rubyRound(x float64, ndigits int) float64 {
	if ndigits <= 0 {
		return math.Round(x)
	}
	if x == 0 {
		return x
	}
	_, binexp := math.Frexp(x)
	if floatRoundOverflow(ndigits, binexp) {
		return x
	}
	if x > 0 && floatRoundUnderflow(ndigits, binexp) {
		return 0
	}
	if ndigits > 14 {
		// MRI goes through a Rational here; nothing in Fuse rounds this fine.
		v, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'f', ndigits, 64), 64)
		return v
	}
	s := math.Pow(10, float64(ndigits))
	return roundHalfUp(x, s) / s
}

// floatRoundOverflow and floatRoundUnderflow are float.c's: whether x has
// fewer digits than asked for (it stays as it is), or so few the answer is
// 0. Go's / truncates toward zero, as C's does.
func floatRoundOverflow(ndigits, binexp int) bool {
	const floatDig = 15 + 2 // DBL_DIG+2
	if binexp > 0 {
		return ndigits >= floatDig-binexp/4
	}
	return ndigits >= floatDig-(binexp/3-1)
}

func floatRoundUnderflow(ndigits, binexp int) bool {
	if binexp > 0 {
		return ndigits < -(binexp/3 + 1)
	}
	return ndigits < -(binexp / 4)
}

// roundHalfUp is float.c's round_half_up(x, s).
func roundHalfUp(x, s float64) float64 {
	xs := x * s
	f := math.Round(xs)
	if s == 1.0 {
		return f
	}
	if x > 0 {
		if (f+0.5)/s <= x {
			f++
		}
		return f
	}
	if (f-0.5)/s >= x {
		f--
	}
	return f
}

// rubyFloatString is Ruby's Float#to_s for the values Fuse prints in labels
// ("18.9", "5.0", "100.0"): the shortest representation that reads back,
// always with a decimal point. Ruby switches to exponent form below 1e-4
// and from 1e16; so does this.
func rubyFloatString(x float64) string {
	if math.IsInf(x, 1) {
		return "Infinity"
	}
	if math.IsInf(x, -1) {
		return "-Infinity"
	}
	if math.IsNaN(x) {
		return "NaN"
	}
	abs := math.Abs(x)
	if abs != 0 && (abs < 1e-4 || abs >= 1e16) {
		s := strconv.FormatFloat(x, 'e', -1, 64) // 1e+16, 1.5e-05
		mant, exp, _ := strings.Cut(s, "e")
		if !strings.Contains(mant, ".") {
			mant += ".0"
		}
		sign := exp[0]
		digits := strings.TrimLeft(exp[1:], "0")
		if len(digits) < 2 {
			digits = strings.Repeat("0", 2-len(digits)) + digits
		}
		return mant + "e" + string(sign) + digits
	}
	s := strconv.FormatFloat(x, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// rubySum is Array#sum over floats: Kahan-Babuska compensated summation
// (array.c ary_add_one / rb_ary_sum), which can differ in the last bit from
// adding left to right — enough to move a .round(2). Fuse sums leg miles and
// hours with it.
func rubySum(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	f, c := 0.0, 0.0
	for _, x := range xs {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			// rb_ary_sum leaves the compensated loop for non-finite values.
			f += x
			continue
		}
		t := f + x
		if math.Abs(f) >= math.Abs(x) {
			c += (f - t) + x
		} else {
			c += (x - t) + f
		}
		f = t
	}
	return f + c
}

// rubyToI is #to_i on what Rails hands Fuse as a quantity: an Integer as it
// is, a Float truncated, a String's leading integer ("2abc" is 2, "abc" 0),
// nil 0. ok is false for what has no #to_i (true, a hash), where Fuse would
// fail the request.
func rubyToI(v any) (n int64, ok bool) {
	switch x := v.(type) {
	case nil:
		return 0, true
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, false
		}
		return int64(x), true
	case int:
		return int64(x), true
	case int64:
		return x, true
	case string:
		s := strings.TrimLeft(x, " \t\n\v\f\r")
		i := 0
		if i < len(s) && (s[i] == '-' || s[i] == '+') {
			i++
		}
		start := i
		for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '_' && i > start && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9') {
			i++
		}
		if i == start {
			return 0, true
		}
		n, err := strconv.ParseInt(strings.ReplaceAll(s[:i], "_", ""), 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// floorDiv is Ruby's Integer#/ : it rounds toward negative infinity, where
// Go's / truncates toward zero. They differ only for a negative amount (a
// credit), as in a percentage discount on one.
func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// rubyStrip is String#strip: whitespace and NUL off both ends.
func rubyStrip(s string) string {
	return strings.Trim(s, "\x00\t\n\v\f\r ")
}

// blank is ActiveSupport's #blank? for a string: empty or only whitespace.
func blank(s string) bool {
	return strings.TrimSpace(s) == ""
}
