// Modified for loom, 2026: adds ClampToInt (golua PR #136); hexadecimal integers are accumulated digit by digit, wrapping as Lua specifies, instead of the last 16 digits being parsed with strconv.ParseUint; a second sign after a leading "+" is rejected.
// See third_party/golua/README.md for the list of changes from upstream.

package runtime

import (
	"math"
	"strconv"
	"strings"
)

// NumberType represents a type of number
type NumberType uint16

const (
	// IsFloat is the type of Floats
	IsFloat NumberType = 1 << iota
	// IsInt is the type of Ints
	IsInt
	// NaN is the type of values which are not numbers
	NaN
	// NaI is a type for values which a not Ints
	NaI
)

func numberType(v Value) NumberType {
	switch v.iface.(type) {
	case int64:
		return IsInt
	case float64:
		return IsFloat
	default:
		return NaN
	}
}

// ToNumber returns x as a Float or Int, and the type (IsFloat, IsInt or NaN).
func ToNumber(v Value) (int64, float64, NumberType) {
	switch v.iface.(type) {
	case int64:
		return v.AsInt(), 0, IsInt
	case float64:
		return 0, v.AsFloat(), IsFloat
	case string:
		s := v.AsString()
		return StringToNumber(strings.TrimSpace(s))
	}
	return 0, 0, NaN
}

// ToNumberValue returns the Value v as a Float or Int, and if it is a number.
// If it is not a number, it returns v unchanged and NaN.
func ToNumberValue(v Value) (Value, NumberType) {
	switch v.NumberType() {
	case IntType:
		return v, IsInt
	case FloatType:
		return v, IsFloat
	}
	if s, ok := v.TryString(); ok {
		n, f, tp := StringToNumber(strings.TrimSpace(s))
		switch tp {
		case IsInt:
			return IntValue(n), IsInt
		case IsFloat:
			return FloatValue(f), IsFloat
		}
	}
	return v, NaN
}

// ToInt returns v as an Int and true if v is actually a valid integer.
func ToInt(v Value) (int64, bool) {
	if n, ok := v.TryInt(); ok {
		return n, true
	}
	if f, ok := v.TryFloat(); ok {
		n, tp := FloatToInt(f)
		return n, tp == IsInt
	}
	if s, ok := v.TryString(); ok {
		n, tp := stringToInt(s)
		return n, tp == IsInt
	}
	return 0, false
}

// ClampToInt converts a Lua integer to an int, saturating at the bounds of int
// instead of wrapping. Where int is 32 bits, a plain int(n) turns a large
// position, count or level into an unrelated small one: string.sub(s, 2^32+1)
// would start at position 1. A saturated value stays out of range, which is
// what callers already check for. Where int is 64 bits this is a plain
// conversion.
func ClampToInt(n int64) int {
	if n > math.MaxInt {
		return math.MaxInt
	}
	if n < math.MinInt {
		return math.MinInt
	}
	return int(n)
}

// ToIntNoString returns v as an Int and true if v is actually a valid integer.
func ToIntNoString(v Value) (int64, bool) {
	switch v.iface.(type) {
	case int64:
		return v.AsInt(), true
	case float64:
		n, tp := FloatToInt(v.AsFloat())
		return n, tp == IsInt
	}
	return 0, false
}

// ToFloat returns v as a FLoat and true if v is a valid float.
func ToFloat(v Value) (float64, bool) {
	switch v.iface.(type) {
	case int64:
		return float64(v.AsInt()), true
	case float64:
		return v.AsFloat(), true
	case string:
		n, f, tp := StringToNumber(v.AsString())
		switch tp {
		case IsInt:
			return float64(n), true
		case IsFloat:
			return f, true
		}
	}
	return 0, false
}

// FloatToInt turns a float64 into an int64 if possible.
func FloatToInt(f float64) (int64, NumberType) {
	if math.Trunc(f) != f {
		return 0, NaI
	}
	if f < float64(math.MinInt64) || f >= float64(math.MaxInt64) {
		return 0, NaI
	}
	return int64(f), IsInt
}

//
// String
//

// stringToInt turns a string into and int64 if possible.
func stringToInt(s string) (int64, NumberType) {
	n, f, tp := StringToNumber(s)
	switch tp {
	case IsInt:
		return n, IsInt
	case IsFloat:
		return FloatToInt(f)
	}
	return 0, NaN
}

func StringToNumber(s string) (n int64, f float64, tp NumberType) {
	s = strings.TrimSpace(s)
	var err error
	if len(s) == 0 {
		tp = NaN
		return
	}
	var i0 = 0
	// If the string starts with -?0[xX] then it may be an hex number
	if s[0] == '+' {
		s = s[1:]
		// One sign only: strconv would accept the second one of "+-10" or
		// "++10", which reference Lua rejects.
		if len(s) > 0 && (s[0] == '-' || s[0] == '+') {
			tp = NaN
			return
		}
	} else if s[0] == '-' {
		i0++
	}
	var isHex = len(s) >= 2+i0 && s[i0] == '0' && (s[i0+1] == 'x' || s[i0+1] == 'X')
	var isFloat = isHex && strings.ContainsAny(s, ".pP") || !isHex && strings.ContainsAny(s, ".eE")
	if isFloat {
		// This is to make strconv.ParseFloat happy
		if isHex && !strings.ContainsAny(s, "pP") {
			s = s + "p0"
		}
		f, err = strconv.ParseFloat(s, 64)
		if err != nil && f == 0 {
			tp = NaN
			return
		}
		tp = IsFloat
		return
	}

	// If s is an hex number, its digits are accumulated in a uint64 that
	// wraps around on overflow, as the Lua manual specifies for hexadecimal
	// integers (§3.1) and as reference Lua's l_str2int does. Every digit is
	// checked, including those that overflow out of the result.
	if isHex {
		us := s[2+i0:]
		if len(us) == 0 {
			tp = NaN
			return
		}
		var un uint64
		for j := 0; j < len(us); j++ {
			d, ok := hexDigitValue(us[j])
			if !ok {
				tp = NaN
				return
			}
			un = un<<4 | d
		}
		n = int64(un)
		if s[0] == '-' {
			n = -n
		}
		tp = IsInt
		return
	}
	n, err = strconv.ParseInt(s, 10, 64)
	if err != nil {
		if err.(*strconv.NumError).Err == strconv.ErrRange {
			// Try a float instead
			f, err = strconv.ParseFloat(s, 64)
			if err == nil || f != 0 {
				tp = IsFloat
				return
			}
		}
		tp = NaN
		return
	}
	tp = IsInt
	return
}

// hexDigitValue returns the value of the hexadecimal digit c.
func hexDigitValue(c byte) (uint64, bool) {
	switch {
	case '0' <= c && c <= '9':
		return uint64(c - '0'), true
	case 'a' <= c && c <= 'f':
		return uint64(c-'a') + 10, true
	case 'A' <= c && c <= 'F':
		return uint64(c-'A') + 10, true
	}
	return 0, false
}
