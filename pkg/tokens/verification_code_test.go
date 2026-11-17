package tokens

import (
	"testing"
)

func TestGenerateVerificationCode(t *testing.T) {
	for _, length := range []int{4, 6} {
		code, err := GenerateVerificationCode(length)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(code) != length {
			t.Errorf("asked for %d digits, got %q", length, code)
		}
		for _, c := range code {
			if c < '0' || c > '9' {
				t.Errorf("not a digit in %q", code)
			}
		}
	}
}

// Each digit must be uniform. Taking a random byte modulo 10 is not: 256 is not a multiple of
// 10, so six of the ten digits come out 26 times in 256 and the other four 25 times.
//
// A chi-square test over 600,000 digits tells the two apart: for uniform digits the statistic
// (9 degrees of freedom) exceeds 55 with probability about 1e-8, while the modulo bias puts its
// expected value near 220.
func TestGenerateVerificationCodeDigitsAreUniform(t *testing.T) {
	const codes = 100000
	const length = 6
	counts := map[rune]int{}
	for i := 0; i < codes; i++ {
		code, err := GenerateVerificationCode(length)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, c := range code {
			counts[c]++
		}
	}
	if len(counts) != 10 {
		t.Fatalf("expected the ten digits, got %v", counts)
	}
	expected := float64(codes*length) / 10
	chi2 := 0.0
	for _, n := range counts {
		d := float64(n) - expected
		chi2 += d * d / expected
	}
	if chi2 > 55 {
		t.Errorf("the digits are not uniform: chi-square %.1f over 9 degrees of freedom, counts %v", chi2, counts)
	}
}
