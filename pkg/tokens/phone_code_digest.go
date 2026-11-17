package tokens

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	b64 "encoding/base64"
	"encoding/hex"
	"errors"
	"os"
)

// A phone verification code is never stored. The account keeps a keyed digest of it instead, so
// that a copy of the database (a backup, a replica, a dump) yields no code that still works.
//
// A plain hash would not do: a code has six digits, so anyone holding the hash finds the code by
// trying all million of them. The digest is an HMAC under a key derived from JWT_TOKEN_KEY, which
// lives in the service's environment and not in the database; without it the digest says
// nothing about the code.
//
// The digest also covers the account ID, so a digest copied onto another account matches no code
// there.

const phoneCodeDigestPrefix = "hmac-sha256:"

// phoneCodeKeyLabel separates the derived key from the JWT signatures made with the same secret:
// a JWT signing input always contains a ".", and the label has none.
const phoneCodeKeyLabel = "influenzanet user-management phone verification code v1"

// ErrNoPhoneCodeKey reports that JWT_TOKEN_KEY is missing or is not a valid key, so no code can
// be stored or checked.
var ErrNoPhoneCodeKey = errors.New("JWT_TOKEN_KEY is missing or invalid: phone verification codes cannot be digested")

// phoneCodeKey derives the digest key from JWT_TOKEN_KEY, read on every call, under the same rule
// as the JWT key: base64 of at least 32 bytes. Rotating JWT_TOKEN_KEY therefore voids every code
// pending at that moment, which only asks the participant to request a new one.
func phoneCodeKey() ([]byte, error) {
	secret, err := b64.StdEncoding.DecodeString(os.Getenv("JWT_TOKEN_KEY"))
	if err != nil || len(secret) < 32 {
		return nil, ErrNoPhoneCodeKey
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(phoneCodeKeyLabel))
	return mac.Sum(nil), nil
}

// PhoneCodeDigest returns the value stored in place of the phone verification code sent to the
// account userID.
func PhoneCodeDigest(userID, code string) (string, error) {
	key, err := phoneCodeKey()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(userID + "|" + code))
	return phoneCodeDigestPrefix + hex.EncodeToString(mac.Sum(nil)), nil
}

// SamePhoneCodeDigest reports whether the digest of a submitted code equals the stored one, in
// time that does not depend on where they differ. An empty stored digest means no code is
// pending, and matches nothing.
func SamePhoneCodeDigest(stored, submitted string) bool {
	if stored == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(stored), []byte(submitted)) == 1
}
