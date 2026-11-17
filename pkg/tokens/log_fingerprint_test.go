package tokens

import (
	"strings"
	"testing"
)

// The documented operator recipe (sha256sum, first 12 hex characters) must
// keep finding a token in the logs.
func TestLogFingerprintMatchesSha256sumPrefix(t *testing.T) {
	// printf '%s' abc | sha256sum -> ba7816bf8f01cfea414140de5dae2223...
	if got, want := LogFingerprint("abc"), "sha256:ba7816bf8f01"; got != want {
		t.Fatalf("LogFingerprint(abc) = %q, want %q", got, want)
	}
}

func TestLogFingerprintDoesNotRevealTheToken(t *testing.T) {
	for i := 0; i < 50; i++ {
		token, err := GenerateUniqueTokenString()
		if err != nil {
			t.Fatal(err)
		}
		fp := LogFingerprint(token)
		lower := strings.ToLower(fp)
		// No run of 6 characters of the token may appear in the label.
		for start := 0; start+6 <= len(token); start++ {
			if strings.Contains(lower, strings.ToLower(token[start:start+6])) {
				t.Fatalf("fingerprint %q contains %q from token %q", fp, token[start:start+6], token)
			}
		}
	}
}

func TestLogFingerprintIsStableAndTellsTokensApart(t *testing.T) {
	a, err := GenerateUniqueTokenString()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateUniqueTokenString()
	if err != nil {
		t.Fatal(err)
	}
	if LogFingerprint(a) != LogFingerprint(a) {
		t.Fatal("the same token must give the same label, or log lines cannot be matched")
	}
	if LogFingerprint(a) == LogFingerprint(b) {
		t.Fatalf("two tokens gave the same label %q", LogFingerprint(a))
	}
	// Tokens generated in the same millisecond share their time prefix; the
	// label must still differ when only the end of the token does.
	if LogFingerprint(a[:len(a)-1]+"a") == LogFingerprint(a[:len(a)-1]+"b") {
		t.Fatal("tokens differing in their last character gave the same label")
	}
}
