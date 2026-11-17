package models

import (
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

// The login 2FA code has no destination number, so its document carries no phone key.
func TestVerificationCodeHasNoPhoneKey(t *testing.T) {
	raw, err := bson.Marshal(VerificationCode{Code: "123456", Attempts: 1, CreatedAt: 10, ExpiresAt: 20})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := bson.Raw(raw).LookupErr("phone"); err == nil {
		t.Errorf("a login code must not store a phone key: %v", bson.Raw(raw))
	}
}

func TestPhoneVerificationCodeStoresPhoneWhenSet(t *testing.T) {
	raw, err := bson.Marshal(PhoneVerificationCode{CodeHash: "hmac-sha256:00", Phone: "+391230000001"})
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

// A phone code document has no "code" key at all: nothing in the model can hold the code in
// clear.
func TestPhoneVerificationCodeHasNoClearCodeKey(t *testing.T) {
	raw, err := bson.Marshal(PhoneVerificationCode{CodeHash: "hmac-sha256:00", Attempts: 1, CreatedAt: 10, ExpiresAt: 20, Phone: "+391230000001"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := bson.Raw(raw).LookupErr("code"); err == nil {
		t.Errorf("a phone code document must not have a code key: %v", bson.Raw(raw))
	}
	if value, err := bson.Raw(raw).LookupErr("codeHash"); err != nil || value.StringValue() != "hmac-sha256:00" {
		t.Errorf("the digest was not stored under codeHash: %v", bson.Raw(raw))
	}
}

// A document stored before codes were digested holds the code in clear under "code". It must
// read as no code pending, so the clear code can never be checked against anything.
func TestLegacyClearPhoneCodeReadsAsNoCode(t *testing.T) {
	raw, err := bson.Marshal(bson.M{"code": "123456", "attempts": 0, "createdAt": 10, "expiresAt": 20, "phone": "+391230000001"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got PhoneVerificationCode
	if err := bson.Unmarshal(raw, &got); err != nil {
		t.Fatalf("a legacy document must still decode: %v", err)
	}
	if got.CodeHash != "" {
		t.Errorf("a clear legacy code was read as a digest: %+v", got)
	}
}
