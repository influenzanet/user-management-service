package models

import (
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

// The phone verification code and the login 2FA code share this struct. Binding a code to a
// phone number must therefore leave the 2FA document exactly as it was: the new field is
// omitted when empty, so no stored login code gains a key it did not have.
func TestVerificationCodeOmitsPhoneWhenEmpty(t *testing.T) {
	raw, err := bson.Marshal(VerificationCode{Code: "123456", Attempts: 1, CreatedAt: 10, ExpiresAt: 20})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := bson.Raw(raw).LookupErr("phone"); err == nil {
		t.Errorf("a code with no destination number must not store a phone key: %v", bson.Raw(raw))
	}
}

func TestVerificationCodeStoresPhoneWhenSet(t *testing.T) {
	raw, err := bson.Marshal(VerificationCode{Code: "123456", Phone: "+391230000001"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	value, err := bson.Raw(raw).LookupErr("phone")
	if err != nil {
		t.Fatalf("the destination number was not stored: %v", err)
	}
	if got := value.StringValue(); got != "+391230000001" {
		t.Errorf("wrong number stored: %q", got)
	}
}
