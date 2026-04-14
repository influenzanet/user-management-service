package models

import (
	"testing"
)

func TestPhoneVerificationCodeIsIndependentFromLoginCode(t *testing.T) {
	account := Account{}

	// Simulate login 2FA setting a code
	account.VerificationCode = VerificationCode{
		Code:      "111111",
		Attempts:  0,
		CreatedAt: 1000,
		ExpiresAt: 2000,
	}

	// Simulate phone verification setting a different code
	account.PhoneVerificationCode = VerificationCode{
		Code:      "222222",
		Attempts:  0,
		CreatedAt: 1000,
		ExpiresAt: 2000,
	}

	// Both codes must coexist without overwriting each other
	if account.VerificationCode.Code != "111111" {
		t.Errorf("login 2FA code was overwritten: got %q, want %q", account.VerificationCode.Code, "111111")
	}
	if account.PhoneVerificationCode.Code != "222222" {
		t.Errorf("phone verification code was overwritten: got %q, want %q", account.PhoneVerificationCode.Code, "222222")
	}
}

func TestPhoneVerificationCodeClearDoesNotAffectLoginCode(t *testing.T) {
	account := Account{
		VerificationCode: VerificationCode{
			Code:      "111111",
			ExpiresAt: 2000,
		},
		PhoneVerificationCode: VerificationCode{
			Code:      "222222",
			ExpiresAt: 2000,
		},
	}

	// Clear phone code (as VerifyWhatsAppCode does on success)
	account.PhoneVerificationCode = VerificationCode{}

	if account.VerificationCode.Code != "111111" {
		t.Errorf("login 2FA code was cleared when phone code was reset: got %q", account.VerificationCode.Code)
	}
	if account.PhoneVerificationCode.Code != "" {
		t.Errorf("phone code was not cleared: got %q", account.PhoneVerificationCode.Code)
	}
}

func TestLoginCodeClearDoesNotAffectPhoneCode(t *testing.T) {
	account := Account{
		VerificationCode: VerificationCode{
			Code:      "111111",
			ExpiresAt: 2000,
		},
		PhoneVerificationCode: VerificationCode{
			Code:      "222222",
			ExpiresAt: 2000,
		},
	}

	// Clear login code (as LoginWithEmail does on success)
	account.VerificationCode = VerificationCode{}

	if account.PhoneVerificationCode.Code != "222222" {
		t.Errorf("phone code was cleared when login code was reset: got %q", account.PhoneVerificationCode.Code)
	}
	if account.VerificationCode.Code != "" {
		t.Errorf("login code was not cleared: got %q", account.VerificationCode.Code)
	}
}

func TestPhoneVerificationCodeZeroValue(t *testing.T) {
	account := Account{}

	// Zero value: both codes empty
	if account.PhoneVerificationCode.Code != "" {
		t.Errorf("expected empty phone verification code on zero-value Account, got %q", account.PhoneVerificationCode.Code)
	}
	if account.VerificationCode.Code != "" {
		t.Errorf("expected empty verification code on zero-value Account, got %q", account.VerificationCode.Code)
	}
}

func TestPhoneVerificationAttemptsZeroValue(t *testing.T) {
	account := Account{}

	// Zero value: nil slice — HasMoreAttemptsRecently(nil, ...) returns false
	if account.PhoneVerificationAttempts != nil {
		t.Error("expected nil PhoneVerificationAttempts on zero-value Account")
	}
}

func TestPhoneVerificationAttemptsNotExposedInAPI(t *testing.T) {
	account := Account{
		Type:                      "email-pw",
		AccountID:                 "test@example.com",
		PhoneVerificationAttempts: []int64{1000, 2000, 3000},
	}

	apiAccount := account.ToAPI()

	// PhoneVerificationAttempts must not leak into the API response
	if apiAccount.Type != "email-pw" {
		t.Errorf("Type not mapped: got %q", apiAccount.Type)
	}
	if apiAccount.AccountId != "test@example.com" {
		t.Errorf("AccountId not mapped: got %q", apiAccount.AccountId)
	}
	// api.User_Account has no PhoneVerificationAttempts field — this test
	// ensures the mapping stays correct if ToAPI is modified in the future.
}
