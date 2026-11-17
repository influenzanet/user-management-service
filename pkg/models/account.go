package models

import (
	"github.com/influenzanet/user-management-service/pkg/api"
)

// Account holds information about user authentication methods
type Account struct {
	Type                  string                `bson:"type"`
	AccountID             string                `bson:"accountID"`
	AccountConfirmedAt    int64                 `bson:"accountConfirmedAt"`
	Password              string                `bson:"password"`
	AuthType              string                `bson:"authType"`
	VerificationCode      VerificationCode      `bson:"verificationCode"`      // Used by login 2FA and AutoValidateTempToken
	PhoneVerificationCode PhoneVerificationCode `bson:"phoneVerificationCode"` // Used by phone/WhatsApp verification only
	PreferredLanguage     string                `bson:"preferredLanguage"`

	// Rate limiting
	FailedLoginAttempts       []int64 `bson:"failedLoginAttempts"`
	PasswordResetTriggers     []int64 `bson:"passwordResetTriggers"`
	PhoneVerificationAttempts []int64 `bson:"phoneVerificationAttempts"`
}

// VerificationCode holds the login 2FA code, which AutoValidateTempToken hands back to the
// login link, so it is stored as it is.
type VerificationCode struct {
	Code      string `bson:"code"`
	Attempts  int64  `bson:"attempts"`
	CreatedAt int64  `bson:"createdAt"`
	ExpiresAt int64  `bson:"expiresAt"`
}

// PhoneVerificationCode holds a pending phone verification. The code itself is never stored:
// CodeHash is its keyed digest (tokens.PhoneCodeDigest), and the endpoint compares the digest of
// the code the participant types against it. Documents written before codes were digested carry
// a "code" field and no "codeHash"; they read as no verification in progress.
type PhoneVerificationCode struct {
	CodeHash  string `bson:"codeHash"`
	Attempts  int64  `bson:"attempts"`
	CreatedAt int64  `bson:"createdAt"`
	ExpiresAt int64  `bson:"expiresAt"`
	// Phone binds the code to the number it was sent to, so a code can only ever confirm that
	// number.
	Phone string `bson:"phone,omitempty"`
}

func AccountFromAPI(a *api.User_Account) Account {
	if a == nil {
		return Account{}
	}
	return Account{
		Type:               a.Type,
		AccountID:          a.AccountId,
		AccountConfirmedAt: a.AccountConfirmedAt,
		PreferredLanguage:  a.PreferredLanguage,
	}
}

// ToAPI converts the object from DB to API format
func (a Account) ToAPI() *api.User_Account {
	return &api.User_Account{
		Type:               a.Type,
		AccountId:          a.AccountID,
		AccountConfirmedAt: a.AccountConfirmedAt,
		PreferredLanguage:  a.PreferredLanguage,
	}
}
