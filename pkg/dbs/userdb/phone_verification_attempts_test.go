package userdb

import (
	"errors"
	"testing"
	"time"

	"github.com/influenzanet/user-management-service/pkg/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// Attempts belong to one verification code: IncrementVerificationCodeAttempts charges only the
// code the caller read, and DeletePhoneAfterFailedAttempts removes the number only while that
// code is still the account's, at its cap, with the number unverified.

const attemptsTestPhone = "+393230020001"

// addAttemptsTestUser stores an account with attemptsTestPhone, confirmed or not, and code.
func addAttemptsTestUser(t *testing.T, confirmed bool, code models.PhoneVerificationCode) string {
	t.Helper()
	user := models.User{
		Account: models.Account{
			AccountID:                 primitive.NewObjectID().Hex() + "@test.com",
			PhoneVerificationCode:     code,
			PhoneVerificationAttempts: []int64{},
		},
		ContactPreferences: models.ContactPreferences{PreferredChannels: []string{models.ChannelEmail, models.ChannelWhatsApp}},
	}
	user.AddNewEmail(user.Account.AccountID, true)
	user.AddNewPhone(attemptsTestPhone, confirmed)
	id, err := testDBService.AddUser(testInstanceID, user)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func attemptsTestCode(attempts int64) models.PhoneVerificationCode {
	now := time.Now().Unix()
	return models.PhoneVerificationCode{CodeHash: "digest-a", Attempts: attempts, CreatedAt: now, ExpiresAt: now + 300, Phone: attemptsTestPhone}
}

func phoneOfAttemptsTestUser(t *testing.T, id string) (models.ContactInfo, bool) {
	t.Helper()
	user, err := testDBService.GetUserByID(testInstanceID, id)
	if err != nil {
		t.Fatal(err)
	}
	return user.FindContactInfoByTypeAndAddr(models.ContactTypePhone, attemptsTestPhone)
}

// The increment charges the code the caller read and no other: not a code since replaced
// (another digest, number or creation time), and not a code since cleared by a verification.
// Fails if the filter drops any part of the code.
func TestIncrementVerificationCodeAttemptsChargesOnlyTheCodeRead(t *testing.T) {
	read := attemptsTestCode(0)
	for _, tt := range []struct {
		name   string
		stored func() models.PhoneVerificationCode
	}{
		{"another digest", func() models.PhoneVerificationCode { c := read; c.CodeHash = "digest-b"; return c }},
		{"another number", func() models.PhoneVerificationCode { c := read; c.Phone = "+393230020002"; return c }},
		{"another creation time", func() models.PhoneVerificationCode { c := read; c.CreatedAt++; return c }},
		{"cleared by a verification", func() models.PhoneVerificationCode { return models.PhoneVerificationCode{} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stored := tt.stored()
			id := addAttemptsTestUser(t, false, stored)
			if _, err := testDBService.IncrementVerificationCodeAttempts(testInstanceID, id, read, 3); !errors.Is(err, mongo.ErrNoDocuments) {
				t.Fatalf("a code the account no longer holds was charged: %v", err)
			}
			user, _ := testDBService.GetUserByID(testInstanceID, id)
			if user.Account.PhoneVerificationCode.Attempts != stored.Attempts {
				t.Fatalf("attempts moved to %d", user.Account.PhoneVerificationCode.Attempts)
			}
		})
	}
	t.Run("the code read", func(t *testing.T) {
		id := addAttemptsTestUser(t, false, read)
		user, err := testDBService.IncrementVerificationCodeAttempts(testInstanceID, id, read, 3)
		if err != nil {
			t.Fatal(err)
		}
		if user.Account.PhoneVerificationCode.Attempts != 1 {
			t.Fatalf("attempts = %d, want 1", user.Account.PhoneVerificationCode.Attempts)
		}
	})
	t.Run("at the cap", func(t *testing.T) {
		atCap := attemptsTestCode(3)
		id := addAttemptsTestUser(t, false, atCap)
		if _, err := testDBService.IncrementVerificationCodeAttempts(testInstanceID, id, atCap, 3); !errors.Is(err, mongo.ErrNoDocuments) {
			t.Fatalf("a code at its cap was charged: %v", err)
		}
	})
}

// The removal after failed attempts takes the number only from an account that still holds the
// code at its cap, with the number unverified; in every other case it changes nothing.
// Fails if any condition of the filter is dropped, or the removal stops dropping the phone and
// the whatsapp channel.
func TestDeletePhoneAfterFailedAttempts(t *testing.T) {
	atCap := attemptsTestCode(3)
	for _, tt := range []struct {
		name      string
		confirmed bool
		stored    models.PhoneVerificationCode
		read      models.PhoneVerificationCode
	}{
		{"below the cap", false, attemptsTestCode(2), attemptsTestCode(2)},
		{"a verified number", true, atCap, atCap},
		{"another code at the cap", false, func() models.PhoneVerificationCode { c := atCap; c.CodeHash = "digest-b"; return c }(), atCap},
		{"a code cleared by a verification", true, models.PhoneVerificationCode{}, atCap},
	} {
		t.Run(tt.name, func(t *testing.T) {
			id := addAttemptsTestUser(t, tt.confirmed, tt.stored)
			removed, err := testDBService.DeletePhoneAfterFailedAttempts(testInstanceID, id, tt.read, 3)
			if err != nil || removed {
				t.Fatalf("removed=%v err=%v, want nothing removed", removed, err)
			}
			if _, found := phoneOfAttemptsTestUser(t, id); !found {
				t.Fatal("the phone was removed")
			}
		})
	}
	t.Run("the code read, at the cap, unverified", func(t *testing.T) {
		id := addAttemptsTestUser(t, false, atCap)
		removed, err := testDBService.DeletePhoneAfterFailedAttempts(testInstanceID, id, atCap, 3)
		if err != nil || !removed {
			t.Fatalf("removed=%v err=%v, want the phone removed", removed, err)
		}
		if _, found := phoneOfAttemptsTestUser(t, id); found {
			t.Fatal("the phone is still there")
		}
		user, _ := testDBService.GetUserByID(testInstanceID, id)
		if user.ContactPreferences.HasPreferredChannel(models.ChannelWhatsApp) {
			t.Error("the whatsapp channel stayed")
		}
		if user.Account.PhoneVerificationCode.CodeHash != "" {
			t.Error("the code stayed")
		}
	})
}
