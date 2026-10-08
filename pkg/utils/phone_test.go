package utils

import "testing"

func TestNormalizePhone(t *testing.T) {
	valid := []struct {
		name  string
		input string
		want  string
	}{
		// Italy: mobiles have no trunk zero, landlines keep their leading 0
		{"IT mobile plus and spaces", "+39 331 622 1419", "+393316221419"},
		{"IT mobile 00 prefix", "0039 331 6221419", "+393316221419"},
		{"IT mobile dashes", "+39-331-622-1419", "+393316221419"},
		{"IT mobile parenthesised plus", "(+39) 3316221419", "+393316221419"},
		{"IT mobile dots", "+39.331.622.1419", "+393316221419"},
		{"IT mobile already E.164", "+393316221419", "+393316221419"},
		{"IT mobile surrounding blanks", "  +39 331 6221419\n", "+393316221419"},
		{"IT landline keeps the 0", "+39 06 1234 5678", "+390612345678"},
		{"IT landline 00 prefix", "0039 06 12345678", "+390612345678"},
		// United Kingdom: trunk zero dropped, also when written in parentheses
		{"UK landline with (0)", "+44 (0)20 7946 0018", "+442079460018"},
		{"UK landline 00 prefix", "0044 20 7946 0018", "+442079460018"},
		{"UK landline trunk zero without parentheses", "+44 020 7946 0018", "+442079460018"},
		{"UK mobile", "+44 7911 123456", "+447911123456"},
		{"UK mobile with (0)", "+44 (0)7911 123456", "+447911123456"},
		// Germany
		{"DE with (0)", "+49 (0)30 123456", "+4930123456"},
		{"DE trunk zero", "+49 030 123456", "+4930123456"},
		{"DE 00 prefix", "0049 30 123456", "+4930123456"},
		{"DE mobile", "+49 151 23456789", "+4915123456789"},
		// France
		{"FR with (0)", "+33 (0)6 12 34 56 78", "+33612345678"},
		{"FR trunk zero", "+33 06 12 34 56 78", "+33612345678"},
		{"FR 00 prefix", "0033 6 12 34 56 78", "+33612345678"},
		// Netherlands
		{"NL mobile with (0)", "+31 (0)6 12345678", "+31612345678"},
		{"NL mobile 00 prefix", "0031 6 12345678", "+31612345678"},
		// Belgium
		{"BE mobile with (0)", "+32 (0)470 12 34 56", "+32470123456"},
		{"BE mobile trunk zero", "+32 0470 12 34 56", "+32470123456"},
		// Switzerland
		{"CH mobile with (0)", "+41 (0)78 123 45 67", "+41781234567"},
		{"CH mobile 00 prefix", "0041 78 123 45 67", "+41781234567"},
		// Spain: no trunk prefix
		{"ES mobile", "+34 612 34 56 78", "+34612345678"},
		{"ES mobile 00 prefix", "0034 612345678", "+34612345678"},
		// North America
		{"US parentheses and dash", "+1 (415) 555-2671", "+14155552671"},
		{"US 00 prefix", "001 415 555 2671", "+14155552671"},
		{"US dots", "+1.415.555.2671", "+14155552671"},
	}
	for _, tt := range valid {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizePhone(tt.input)
			if err != nil {
				t.Fatalf("NormalizePhone(%q) returned error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("NormalizePhone(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}

	invalid := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"blanks only", "   "},
		{"letters", "+39 abc def ghij"},
		{"letters mixed in", "+39 331 622 14X9"},
		{"extension", "+39 06 12345678 ext 12"},
		{"too short", "+39 331"},
		{"plus only", "+"},
		{"no international prefix", "3316221419"},
		{"national format with trunk zero", "020 7946 0018"},
		{"unknown country code", "+999 123456789"},
		{"too long", "+39 331 622 1419 5555 5555"},
		{"not valid for its region", "+39 331 622"},
		{"plus in the middle", "39+3316221419"},
		{"double prefix", "+0039 331 6221419"},
		{"only 00", "00"},
		{"non ascii digits", "+39 ٣٣١ ٦٢٢ ١٤١٩"},
	}
	for _, tt := range invalid {
		t.Run("invalid "+tt.name, func(t *testing.T) {
			if got, err := NormalizePhone(tt.input); err == nil {
				t.Errorf("NormalizePhone(%q) = %q, want an error", tt.input, got)
			}
		})
	}
}

// All the ways of writing one telephone must produce one stored value.
func TestNormalizePhoneAllFormsAgree(t *testing.T) {
	groups := map[string][]string{
		"+393316221419": {"+393316221419", "+39 331 622 1419", "0039 331 6221419", "+39-331-622-1419", "(+39) 3316221419", "0039-331-622-1419"},
		"+390612345678": {"+390612345678", "+39 06 1234 5678", "0039 06 12345678", "+39 (06) 1234-5678"},
		"+442079460018": {"+442079460018", "+44 (0)20 7946 0018", "0044 20 7946 0018", "+44 020 7946 0018", "+44 (0) 20-7946-0018"},
		"+4930123456":   {"+4930123456", "+49 (0)30 123456", "+49 030 123456", "0049 30 123456"},
		"+33612345678":  {"+33612345678", "+33 (0)6 12 34 56 78", "0033 6 12 34 56 78", "+33 06.12.34.56.78"},
		"+14155552671":  {"+14155552671", "+1 (415) 555-2671", "001 415 555 2671", "+1 415 555 2671"},
	}
	for want, forms := range groups {
		for _, form := range forms {
			got, err := NormalizePhone(form)
			if err != nil {
				t.Errorf("NormalizePhone(%q) returned error: %v", form, err)
				continue
			}
			if got != want {
				t.Errorf("NormalizePhone(%q) = %q, want %q", form, got, want)
			}
			// Normalising is idempotent, which the migration tool relies on.
			again, err := NormalizePhone(got)
			if err != nil || again != got {
				t.Errorf("NormalizePhone(%q) = %q, %v, want it unchanged", got, again, err)
			}
		}
	}
}

func TestSamePhone(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"+393316221419", "0039 331 6221419", true},
		{"+442079460018", "+44 (0)20 7946 0018", true},
		{"+393316221419", "+393316221418", false},
		{"+393316221419", "+34612345678", false},
		// A value that cannot be normalised is only equal to itself
		{"garbage", "garbage", true},
		{"garbage", "+393316221419", false},
		{"", "", true},
		{"", "+393316221419", false},
	}
	for _, tt := range tests {
		if got := SamePhone(tt.a, tt.b); got != tt.want {
			t.Errorf("SamePhone(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
