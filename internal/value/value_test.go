package value

import (
	"encoding/json"
	"math/big"
	"strings"
	"sync"
	"testing"
)

func mustParse(t *testing.T, input string) Value {
	t.Helper()
	v, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func mustDecimal(t *testing.T, v Value) string {
	t.Helper()
	decimal, err := v.ExactDecimal()
	if err != nil {
		t.Fatal(err)
	}
	return decimal
}

func TestParseBoundaries(t *testing.T) {
	for _, input := range []string{"0", "0.000000000000000000000001", strings.Repeat("9", 32), "0001.2300"} {
		if _, err := Parse(input); err != nil {
			t.Errorf("Parse(%q): %v", input, err)
		}
	}
	for _, input := range []string{"", " ", "-0", "+1", ".5", "1.", "1/3", "NaN", "Infinity", "1e3", "1.2.3", "0." + strings.Repeat("0", 25), strings.Repeat("9", 33)} {
		if _, err := Parse(input); err == nil {
			t.Errorf("Parse(%q) should fail", input)
		}
	}
	if _, err := ParsePositive("0.0000"); err == nil {
		t.Fatal("zero was accepted as positive")
	}
}

func TestJSONExponentAndStringBoundary(t *testing.T) {
	for input, want := range map[string]string{
		"1.25e2":     "125",
		"1.25E-2":    "0.0125",
		"0e+3":       "0",
		"1e-24":      "0.000000000000000000000001",
		"1.0000e-24": "0.000000000000000000000001",
		"1.23e+2":    "123",
	} {
		v, err := ParseJSONNumber(input)
		if err != nil || mustDecimal(t, v) != want {
			t.Errorf("ParseJSONNumber(%q) = %q, %v; want %q", input, mustDecimal(t, v), err, want)
		}
	}
	for _, input := range []string{"01", "-0", "1e-25", "1e1000", "1e", "1e2e3", "1.2.3e1"} {
		if _, err := ParseJSONNumber(input); err == nil {
			t.Errorf("ParseJSONNumber(%q) should fail", input)
		}
	}
	var v Value
	if err := json.Unmarshal([]byte(`"123.45"`), &v); err != nil || mustDecimal(t, v) != "123.45" {
		t.Fatalf("JSON string = %q, %v", mustDecimal(t, v), err)
	}
	if err := json.Unmarshal([]byte(`123.45`), &v); err == nil {
		t.Fatal("JSON number accepted for a financial value")
	}
	encoded, err := json.Marshal(v)
	if err != nil || string(encoded) != `"123.45"` {
		t.Fatalf("marshal = %s, %v", encoded, err)
	}
}

func TestArithmeticQuantizationAndHalfEven(t *testing.T) {
	base := mustParse(t, "1.235")
	other := mustParse(t, "0.005")
	if got := mustDecimal(t, base.Add(other)); got != "1.24" {
		t.Fatalf("sum = %s", got)
	}
	if got := mustDecimal(t, base.Sub(other)); got != "1.23" {
		t.Fatalf("difference = %s", got)
	}
	if got := mustDecimal(t, base.Mul(mustParse(t, "2"))); got != "2.47" {
		t.Fatalf("product = %s", got)
	}
	third, err := mustParse(t, "1").Divide(mustParse(t, "3"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := third.ExactDecimal(); err == nil {
		t.Fatal("repeating decimal was treated as exact finite decimal")
	}
	if got, err := third.FormatFixed(10); err != nil || got != "0.3333333333" {
		t.Fatalf("one third = %q, %v", got, err)
	}
	if _, err := base.Divide(Value{}); err == nil {
		t.Fatal("division by zero succeeded")
	}
	for input, want := range map[string]string{"1.225": "1.22", "1.235": "1.24", "2.5": "2.5"} {
		got, err := mustParse(t, input).FormatFixed(2)
		if err != nil || got != want {
			t.Errorf("FormatFixed(%s) = %q, %v; want %q", input, got, err, want)
		}
	}
	floor, err := mustParse(t, "0.07").FloorToIncrement(mustParse(t, "0.03"))
	if err != nil || mustDecimal(t, floor) != "0.06" {
		t.Fatalf("increment floor = %s, %v", mustDecimal(t, floor), err)
	}
	ceil, err := mustParse(t, "0.10001").CeilToScale(4)
	if err != nil || mustDecimal(t, ceil) != "0.1001" {
		t.Fatalf("scale ceil = %s, %v", mustDecimal(t, ceil), err)
	}
	if got := mustDecimal(t, base); got != "1.235" {
		t.Fatalf("input mutated: %s", got)
	}
}

func TestIntegerUnitsWithNonPowerOfTenIncrement(t *testing.T) {
	increment := mustParse(t, "0.03")
	units, err := mustParse(t, "0.12").Units(increment)
	if err != nil || units.Cmp(big.NewInt(4)) != 0 {
		t.Fatalf("units = %v, %v", units, err)
	}
	value, err := FromUnits(units, increment)
	if err != nil || mustDecimal(t, value) != "0.12" {
		t.Fatalf("from units = %s, %v", mustDecimal(t, value), err)
	}
	units.SetInt64(100)
	if got := mustDecimal(t, value); got != "0.12" {
		t.Fatalf("result changed with caller-owned big.Int: %s", got)
	}
	if _, err := mustParse(t, "0.1").Units(increment); err == nil {
		t.Fatal("misaligned value had integer units")
	}
}

func TestCopiedValueIsSafeForConcurrentReads(t *testing.T) {
	base := mustParse(t, "123.456")
	var group sync.WaitGroup
	for range 16 {
		group.Add(1)
		go func(copy Value) {
			defer group.Done()
			for range 100 {
				if got, err := copy.FormatFixed(2); err != nil || got != "123.46" {
					t.Errorf("concurrent format = %q, %v", got, err)
					return
				}
			}
		}(base)
	}
	group.Wait()
}

func FuzzDecimalInputNeverPanicsOrChangesValue(f *testing.F) {
	f.Add("0.0001")
	f.Add("1e-24")
	f.Add("1/3")
	f.Add("-0")
	f.Fuzz(func(t *testing.T, input string) {
		for _, parse := range []func(string) (Value, error){Parse, ParseJSONNumber} {
			parsed, err := parse(input)
			if err != nil {
				continue
			}
			canonical, err := parsed.ExactDecimal()
			if err != nil {
				t.Fatal(err)
			}
			again, err := Parse(canonical)
			if err != nil || parsed.Compare(again) != 0 {
				t.Fatalf("round-trip changed value: %q -> %q, %v", input, canonical, err)
			}
		}
	})
}
