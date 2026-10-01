// Package value provides immutable exact values for financial calculations.
package value

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

const (
	DefaultReportScale   = 10
	maxSignificantDigits = 32
	maxFractionalDigits  = 24
	maxInputLength       = 128
)

// Value is an exact rational. Its zero value is zero, and no mutable pointer is exposed.
type Value struct{ number *big.Rat }

func fromRat(number *big.Rat) Value { return Value{number: number} }

func (v Value) rat() *big.Rat {
	if v.number == nil {
		return new(big.Rat)
	}
	return new(big.Rat).Set(v.number)
}

// Parse accepts a nonnegative plain decimal with at most 32 significant and 24 fractional digits.
func Parse(input string) (Value, error) {
	if input == "" || len(input) > maxInputLength {
		return Value{}, errors.New("decimal must contain 1 to 128 characters")
	}
	dot := strings.IndexByte(input, '.')
	if dot == 0 || dot == len(input)-1 || strings.Count(input, ".") > 1 {
		return Value{}, errors.New("decimal must use plain unsigned notation")
	}
	fractional := 0
	digits := input
	if dot >= 0 {
		fractional = len(input) - dot - 1
		digits = input[:dot] + input[dot+1:]
	}
	if fractional > maxFractionalDigits {
		return Value{}, errors.New("decimal exceeds 24 fractional digits")
	}
	for _, digit := range digits {
		if digit < '0' || digit > '9' {
			return Value{}, errors.New("decimal must use plain unsigned notation")
		}
	}
	firstNonzero := strings.IndexFunc(digits, func(r rune) bool { return r != '0' })
	if firstNonzero >= 0 && len(digits)-firstNonzero > maxSignificantDigits {
		return Value{}, errors.New("decimal exceeds 32 significant digits")
	}
	numerator, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return Value{}, errors.New("invalid decimal")
	}
	denominator := pow10(fractional)
	return fromRat(new(big.Rat).SetFrac(numerator, denominator)), nil
}

func ParsePositive(input string) (Value, error) {
	v, err := Parse(input)
	if err != nil {
		return Value{}, err
	}
	if v.Sign() <= 0 {
		return Value{}, errors.New("decimal must be positive")
	}
	return v, nil
}

// ParseJSONNumber decodes an exchange JSON number exactly; callers retain the raw token for checksums.
func ParseJSONNumber(input string) (Value, error) {
	if input == "" || len(input) > maxInputLength || strings.HasPrefix(input, "-") {
		return Value{}, errors.New("JSON number must be nonnegative")
	}
	mantissa, exponentText, hasExponent := strings.Cut(input, "e")
	if !hasExponent {
		mantissa, exponentText, hasExponent = strings.Cut(input, "E")
	}
	integer, fraction, hasFraction := strings.Cut(mantissa, ".")
	if integer == "" || (len(integer) > 1 && integer[0] == '0') || (hasFraction && fraction == "") {
		return Value{}, errors.New("invalid JSON number")
	}
	for _, digit := range integer + fraction {
		if digit < '0' || digit > '9' {
			return Value{}, errors.New("invalid JSON number")
		}
	}
	if !hasExponent {
		return Parse(mantissa)
	}
	if exponentText == "" {
		return Value{}, errors.New("invalid JSON number exponent")
	}
	exponent, err := strconv.Atoi(exponentText)
	if err != nil || exponent < -maxInputLength || exponent > maxInputLength {
		return Value{}, errors.New("JSON number exponent is out of range")
	}
	digits := integer + fraction
	point := len(integer) + exponent
	var plain string
	switch {
	case point <= 0:
		plain = "0." + strings.Repeat("0", -point) + digits
	case point >= len(digits):
		plain = digits + strings.Repeat("0", point-len(digits))
	default:
		plain = digits[:point] + "." + digits[point:]
	}
	if strings.Contains(plain, ".") {
		plain = strings.TrimSuffix(strings.TrimRight(plain, "0"), ".")
	}
	return Parse(plain)
}

func (v Value) Sign() int               { return v.rat().Sign() }
func (v Value) Compare(other Value) int { return v.rat().Cmp(other.rat()) }
func (v Value) Add(other Value) Value   { return fromRat(new(big.Rat).Add(v.rat(), other.rat())) }
func (v Value) Sub(other Value) Value   { return fromRat(new(big.Rat).Sub(v.rat(), other.rat())) }
func (v Value) Mul(other Value) Value   { return fromRat(new(big.Rat).Mul(v.rat(), other.rat())) }

func (v Value) Divide(other Value) (Value, error) {
	if other.Sign() == 0 {
		return Value{}, errors.New("division by zero")
	}
	return fromRat(new(big.Rat).Quo(v.rat(), other.rat())), nil
}

// FloorToIncrement rounds a nonnegative value down to a positive increment.
func (v Value) FloorToIncrement(increment Value) (Value, error) {
	if v.Sign() < 0 || increment.Sign() <= 0 {
		return Value{}, errors.New("floor requires a nonnegative value and positive increment")
	}
	quotient := new(big.Rat).Quo(v.rat(), increment.rat())
	units := new(big.Int).Quo(quotient.Num(), quotient.Denom())
	return fromRat(new(big.Rat).Mul(new(big.Rat).SetInt(units), increment.rat())), nil
}

func (v Value) CeilToScale(scale int) (Value, error) {
	return v.quantize(scale, true)
}

func (v Value) FloorToScale(scale int) (Value, error) {
	return v.quantize(scale, false)
}

func (v Value) quantize(scale int, ceil bool) (Value, error) {
	if v.Sign() < 0 || scale < 0 || scale > maxFractionalDigits {
		return Value{}, errors.New("quantization requires a nonnegative value and scale from 0 to 24")
	}
	scaled := new(big.Rat).Mul(v.rat(), new(big.Rat).SetInt(pow10(scale)))
	units, remainder := new(big.Int).QuoRem(scaled.Num(), scaled.Denom(), new(big.Int))
	if ceil && remainder.Sign() != 0 {
		units.Add(units, big.NewInt(1))
	}
	return fromRat(new(big.Rat).SetFrac(units, pow10(scale))), nil
}

// FormatFixed rounds half to even, then strips insignificant trailing zeros.
func (v Value) FormatFixed(scale int) (string, error) {
	if scale < 0 || scale > maxFractionalDigits {
		return "", errors.New("reporting scale must be from 0 to 24")
	}
	number := v.rat()
	negative := number.Sign() < 0
	numerator := new(big.Int).Abs(number.Num())
	numerator.Mul(numerator, pow10(scale))
	units, remainder := new(big.Int).QuoRem(numerator, number.Denom(), new(big.Int))
	twiceRemainder := new(big.Int).Mul(remainder, big.NewInt(2))
	comparison := twiceRemainder.Cmp(number.Denom())
	if comparison > 0 || (comparison == 0 && units.Bit(0) == 1) {
		units.Add(units, big.NewInt(1))
	}
	return formatScaled(units, scale, negative), nil
}

// ExactDecimal returns a canonical decimal or an error for a repeating rational.
func (v Value) ExactDecimal() (string, error) {
	number := v.rat()
	if number.Sign() == 0 {
		return "0", nil
	}
	denominator := new(big.Int).Set(number.Denom())
	twos, fives := 0, 0
	zero := new(big.Int)
	for new(big.Int).Mod(denominator, big.NewInt(2)).Cmp(zero) == 0 {
		denominator.Quo(denominator, big.NewInt(2))
		twos++
	}
	for new(big.Int).Mod(denominator, big.NewInt(5)).Cmp(zero) == 0 {
		denominator.Quo(denominator, big.NewInt(5))
		fives++
	}
	if denominator.Cmp(big.NewInt(1)) != 0 {
		return "", errors.New("rational has no finite decimal representation")
	}
	scale := max(twos, fives)
	numerator := new(big.Int).Abs(number.Num())
	numerator.Mul(numerator, pow10(scale))
	units := new(big.Int).Quo(numerator, number.Denom())
	return formatScaled(units, scale, number.Sign() < 0), nil
}

func (v Value) MarshalJSON() ([]byte, error) {
	decimal, err := v.ExactDecimal()
	if err != nil {
		return nil, err
	}
	return json.Marshal(decimal)
}

func (v *Value) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || data[0] != '"' {
		return errors.New("financial JSON values must be decimal strings")
	}
	var input string
	if err := json.Unmarshal(data, &input); err != nil {
		return fmt.Errorf("decode decimal string: %w", err)
	}
	parsed, err := Parse(input)
	if err != nil {
		return err
	}
	*v = parsed
	return nil
}

func pow10(exponent int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exponent)), nil)
}

func formatScaled(units *big.Int, scale int, negative bool) string {
	digits := units.String()
	if scale > 0 {
		if len(digits) <= scale {
			digits = strings.Repeat("0", scale-len(digits)+1) + digits
		}
		point := len(digits) - scale
		digits = digits[:point] + "." + digits[point:]
		digits = strings.TrimRight(strings.TrimRight(digits, "0"), ".")
	}
	if negative && units.Sign() != 0 {
		return "-" + digits
	}
	return digits
}
