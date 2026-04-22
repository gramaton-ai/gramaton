package setup

import (
	"math"
	"testing"
)

// Parser tests — covers input-hardening fixes that stop silent
// acceptance of values that would disable cost protection (Inf, NaN,
// enormous numbers) or leave users thinking a bad value took effect.

func TestParseMoneyUSD(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    float64
		wantErr bool
	}{
		{"plain number", "5.00", 5.00, false},
		{"dollar prefix", "$5", 5.00, false},
		{"dollar prefix with cents", "$5.50", 5.50, false},
		{"whitespace padded", "  10.00  ", 10.00, false},
		{"zero", "0", 0, false},

		// Invalid inputs: each must error, caller falls back to default.
		{"empty", "", 0, true},
		{"garbage", "abc", 0, true},
		{"negative", "-5", 0, true},
		{"positive infinity", "inf", 0, true},
		{"negative infinity", "-inf", 0, true},
		{"NaN", "nan", 0, true},
		{"1e309 overflows to inf", "1e309", 0, true},
		{"above cap", "10001", 0, true},
		{"far above cap", "1000000", 0, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseMoneyUSD(tc.input)
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
			// Extra safety: on success, never return Inf/NaN.
			if err == nil && (math.IsInf(got, 0) || math.IsNaN(got)) {
				t.Errorf("got non-finite on success: %v", got)
			}
		})
	}
}

func TestParseIntAtLeast(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		min     int
		want    int
		wantErr bool
	}{
		{"valid", "500", 1, 500, false},
		{"min value", "1", 1, 1, false},

		{"below min", "0", 1, 0, true},
		{"empty", "", 1, 0, true},
		{"garbage", "abc", 1, 0, true},
		{"above cap", "1000001", 1, 0, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseIntAtLeast(tc.input, tc.min)
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
