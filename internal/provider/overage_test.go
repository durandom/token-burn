package provider

import "testing"

func TestNumericOverageBalance(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		want  float64
		valid bool
	}{{"62750.0000000000", 62750, true}, {"42.125", 42.125, true}, {"0", 0, true}, {"", 0, false}, {"NaN", 0, false}, {"Inf", 0, false}, {"-1", 0, false}, {"1e999", 0, false}, {"invalid", 0, false}} {
		t.Run(tc.raw, func(t *testing.T) {
			c := &OverageCredits{Balance: tc.raw}
			value, valid := c.NumericBalance()
			if valid != tc.valid || (valid && value != tc.want) {
				t.Fatalf("balance %v %t", value, valid)
			}
			if c.Balance != tc.raw {
				t.Fatal("decimal changed")
			}
		})
	}
	var absent *OverageCredits
	if _, ok := absent.NumericBalance(); ok {
		t.Fatal("nil is not zero")
	}
}
