package tokens

import (
	"crypto/sha256"
	b64 "encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func setPhoneCodeKey(t *testing.T, fill byte) {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = fill
	}
	t.Setenv("JWT_TOKEN_KEY", b64.StdEncoding.EncodeToString(key))
}

func mustDigest(t *testing.T, userID, code string) string {
	t.Helper()
	digest, err := PhoneCodeDigest(userID, code)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return digest
}

func TestPhoneCodeDigest(t *testing.T) {
	const user = "65a000000000000000000001"
	setPhoneCodeKey(t, 0x11)

	t.Run("the same code for the same account gives the same digest", func(t *testing.T) {
		if mustDigest(t, user, "123456") != mustDigest(t, user, "123456") {
			t.Error("the digest is not stable, so no code could ever be checked")
		}
	})

	t.Run("another code gives another digest", func(t *testing.T) {
		if mustDigest(t, user, "123456") == mustDigest(t, user, "123457") {
			t.Error("two codes share a digest")
		}
	})

	t.Run("the digest is bound to the account", func(t *testing.T) {
		if mustDigest(t, user, "123456") == mustDigest(t, "65a000000000000000000002", "123456") {
			t.Error("a digest copied onto another account would match the same code there")
		}
	})

	t.Run("the account ID and the code cannot be shifted into each other", func(t *testing.T) {
		if mustDigest(t, "ab", "c123456") == mustDigest(t, "abc", "123456") {
			t.Error("the separator between account ID and code is missing")
		}
	})

	t.Run("the digest depends on the key", func(t *testing.T) {
		withFirstKey := mustDigest(t, user, "123456")
		setPhoneCodeKey(t, 0x22)
		if mustDigest(t, user, "123456") == withFirstKey {
			t.Error("the digest does not depend on JWT_TOKEN_KEY: a database copy alone would reveal the code")
		}
	})

	t.Run("no unkeyed hash of the code gives the digest", func(t *testing.T) {
		// What someone holding only the database could try: the plain hashes the stored value
		// might be. None of them may reproduce it.
		digest := strings.TrimPrefix(mustDigest(t, user, "123456"), phoneCodeDigestPrefix)
		for _, input := range []string{"123456", user + "|123456", user + "123456"} {
			sum := sha256.Sum256([]byte(input))
			if hex.EncodeToString(sum[:]) == digest {
				t.Errorf("the digest is the plain SHA-256 of %q", input)
			}
		}
	})

	t.Run("format", func(t *testing.T) {
		digest := mustDigest(t, user, "123456")
		if !strings.HasPrefix(digest, "hmac-sha256:") {
			t.Fatalf("missing algorithm prefix: %q", digest)
		}
		raw, err := hex.DecodeString(strings.TrimPrefix(digest, "hmac-sha256:"))
		if err != nil || len(raw) != sha256.Size {
			t.Errorf("not a hex SHA-256 value: %q", digest)
		}
		if strings.Contains(digest, "123456") {
			t.Errorf("the digest contains the code: %q", digest)
		}
	})
}

func TestPhoneCodeDigestRefusesWithoutAValidKey(t *testing.T) {
	for name, value := range map[string]string{
		"missing":    "",
		"not base64": "not base64 at all!",
		"too short":  b64.StdEncoding.EncodeToString(make([]byte, 31)),
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("JWT_TOKEN_KEY", value)
			digest, err := PhoneCodeDigest("65a000000000000000000001", "123456")
			if !errors.Is(err, ErrNoPhoneCodeKey) {
				t.Errorf("expected ErrNoPhoneCodeKey, got %v", err)
			}
			if digest != "" {
				t.Errorf("a digest was returned without a key: %q", digest)
			}
		})
	}

	t.Run("32 bytes is enough", func(t *testing.T) {
		t.Setenv("JWT_TOKEN_KEY", b64.StdEncoding.EncodeToString(make([]byte, 32)))
		if _, err := PhoneCodeDigest("65a000000000000000000001", "123456"); err != nil {
			t.Errorf("a valid key was refused: %v", err)
		}
	})
}

func TestSamePhoneCodeDigest(t *testing.T) {
	setPhoneCodeKey(t, 0x11)
	stored := mustDigest(t, "65a000000000000000000001", "123456")

	if !SamePhoneCodeDigest(stored, mustDigest(t, "65a000000000000000000001", "123456")) {
		t.Error("the right code was refused")
	}
	if SamePhoneCodeDigest(stored, mustDigest(t, "65a000000000000000000001", "654321")) {
		t.Error("a wrong code was accepted")
	}
	if SamePhoneCodeDigest(stored, stored[:len(stored)-1]) {
		t.Error("a prefix of the digest was accepted")
	}
	if SamePhoneCodeDigest("", "") {
		t.Error("with no code pending, an empty digest was accepted")
	}
	if SamePhoneCodeDigest("", stored) {
		t.Error("with no code pending, a digest was accepted")
	}
}
