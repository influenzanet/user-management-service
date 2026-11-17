package tokens

import (
	"crypto/sha256"
	"encoding/hex"
)

// logFingerprintBytes is how much of the SHA-256 digest a fingerprint shows:
// 6 bytes, 12 hex characters.
const logFingerprintBytes = 6

// LogFingerprint returns a short label for a secret token (temp token, refresh
// token) to put in a log line instead of the token itself. Two log lines about
// the same token carry the same label, but the label cannot be used to log in.
//
// The label is "sha256:" followed by the first 12 hex characters of the
// token's SHA-256, so an operator holding a token can find it in the logs with
//
//	printf '%s' "$TOKEN" | sha256sum | cut -c1-12
func LogFingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "sha256:" + hex.EncodeToString(sum[:logFingerprintBytes])
}
