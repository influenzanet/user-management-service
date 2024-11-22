package models

import (
	"github.com/influenzanet/user-management-service/pkg/api"
)

// Account holds information about user authentication methods
type Account struct {
	Type               string           `bson:"type"`
	AccountID          string           `bson:"accountID"`
	AccountConfirmedAt int64            `bson:"accountConfirmedAt"`
	Password           string           `bson:"password"`
	AuthType           string           `bson:"authType"`
	VerificationCode   VerificationCode `bson:"verificationCode"`
	PreferredLanguage  string           `bson:"preferredLanguage"`

	// Rate limiting
	FailedLoginAttempts   []int64 `bson:"failedLoginAttempts"`
	PasswordResetTriggers []int64 `bson:"passwordResetTriggers"`
}

// VerificationCode holds account verification data
type VerificationCode struct {
	Code      string `bson:"code"`
	Attempts  int64  `bson:"attempts"`
	CreatedAt int64  `bson:"createdAt"`
	ExpiresAt int64  `bson:"expiresAt"`
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
