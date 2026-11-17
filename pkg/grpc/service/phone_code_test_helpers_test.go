package service

import (
	"errors"
	"fmt"
	"testing"

	"github.com/influenzanet/user-management-service/pkg/models"
	"github.com/influenzanet/user-management-service/pkg/tokens"
)

// clearPhoneCode is a pending phone code as a test seeds it: in clear, the way the participant
// types it. The account stores only its digest; stored computes it for the account.
type clearPhoneCode struct {
	Code      string
	Attempts  int64
	CreatedAt int64
	ExpiresAt int64
	Phone     string
}

// stored returns what the account userID holds for this code. An empty code stays empty: no
// verification pending.
func (c clearPhoneCode) stored(t *testing.T, userID string) models.PhoneVerificationCode {
	t.Helper()
	stored := models.PhoneVerificationCode{
		Attempts:  c.Attempts,
		CreatedAt: c.CreatedAt,
		ExpiresAt: c.ExpiresAt,
		Phone:     c.Phone,
	}
	if c.Code != "" {
		stored.CodeHash = phoneCodeDigestOf(t, userID, c.Code)
	}
	return stored
}

func phoneCodeDigestOf(t *testing.T, userID, code string) string {
	t.Helper()
	digest, err := tokens.PhoneCodeDigest(userID, code)
	if err != nil {
		t.Fatalf("cannot digest a phone code: %v", err)
	}
	return digest
}

// phoneCodeRecorder is a WhatsApp client mock that remembers the codes it was handed.
type phoneCodeRecorder interface {
	codesSentTo(phone string) []string
}

// sentPhoneCode returns the code the participant received for the account's pending
// verification: of the codes the mock delivered to the number the code is bound to, the one
// whose digest the account stores. Two accounts can wait on the same number, so the last code
// sent there is not necessarily this account's. It fails the test when there is none.
func sentPhoneCode(t *testing.T, s *userManagementServer, userID string) string {
	t.Helper()
	code, err := lookupSentPhoneCode(s, userID)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// lookupSentPhoneCode is sentPhoneCode for a goroutine, which must not stop the test: it returns
// an error instead.
func lookupSentPhoneCode(s *userManagementServer, userID string) (string, error) {
	recorder, ok := s.whatsAppClient.(phoneCodeRecorder)
	if !ok {
		return "", fmt.Errorf("the WhatsApp mock %T does not record codes", s.whatsAppClient)
	}
	user, err := testUserDBService.GetUserByID(testInstanceID, userID)
	if err != nil {
		return "", err
	}
	pending := user.Account.PhoneVerificationCode
	if pending.CodeHash == "" {
		return "", errors.New("no code is pending for the account")
	}
	for _, code := range recorder.codesSentTo(pending.Phone) {
		digest, err := tokens.PhoneCodeDigest(userID, code)
		if err != nil {
			return "", err
		}
		if tokens.SamePhoneCodeDigest(pending.CodeHash, digest) {
			return code, nil
		}
	}
	return "", fmt.Errorf("none of the codes sent to %s matches the one the account stores", pending.Phone)
}
