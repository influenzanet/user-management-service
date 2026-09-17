package userdb

import (
	"errors"
	"testing"
	"time"

	"github.com/influenzanet/user-management-service/pkg/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// F-02: a phone verification code is stored on the account without the number it was sent to,
// so it confirms whichever unverified phone the account happens to carry when it is used. The
// two writers of that state are guarded here: the code can only be stored while the number it
// is meant for is still pending, and only that number can be confirmed with it.

const (
	bindingPhoneSent    = "+391230000701" // the number a code was sent to
	bindingPhoneCurrent = "+391230000702" // the number the account carries afterwards
)

func bindingCode(code string, phone string) models.VerificationCode {
	now := time.Now().Unix()
	return models.VerificationCode{Code: code, CreatedAt: now, ExpiresAt: now + 300, Phone: phone}
}

func bindingUser(t *testing.T, accountID string, phone string, phoneConfirmedAt int64) string {
	t.Helper()
	contacts := []models.ContactInfo{
		{ID: primitive.NewObjectID(), Type: models.ContactTypeEmail, Email: accountID, ConfirmedAt: time.Now().Unix()},
	}
	if phone != "" {
		contacts = append(contacts, models.ContactInfo{
			ID: primitive.NewObjectID(), Type: models.ContactTypePhone, Phone: phone, ConfirmedAt: phoneConfirmedAt,
		})
	}
	return addReserveTestUser(t, accountID, []int64{}, contacts)
}

func storedCode(t *testing.T, id string) models.VerificationCode {
	t.Helper()
	user, err := testDBService.GetUserByID(testInstanceID, id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return user.Account.PhoneVerificationCode
}

func TestDbSetPhoneVerificationCodeRequiresPendingPhone(t *testing.T) {
	t.Run("stores a code for the number that is still pending", func(t *testing.T) {
		id := bindingUser(t, "binding_set_ok@test.com", bindingPhoneSent, 0)
		if err := testDBService.SetPhoneVerificationCode(testInstanceID, id, bindingCode("111111", bindingPhoneSent)); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := storedCode(t, id); got.Code != "111111" || got.Phone != bindingPhoneSent {
			t.Errorf("code not bound to the number it is for: %+v", got)
		}
	})

	t.Run("refuses a code for a number that is no longer on the account", func(t *testing.T) {
		id := bindingUser(t, "binding_set_replaced@test.com", bindingPhoneCurrent, 0)
		if err := testDBService.SetPhoneVerificationCode(testInstanceID, id, bindingCode("222222", bindingPhoneCurrent)); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err := testDBService.SetPhoneVerificationCode(testInstanceID, id, bindingCode("333333", bindingPhoneSent))
		if !errors.Is(err, ErrPhoneNotPending) {
			t.Errorf("expected ErrPhoneNotPending, got %v", err)
		}
		if got := storedCode(t, id); got.Code != "222222" {
			t.Errorf("a refused store still overwrote the pending code: %+v", got)
		}
	})

	t.Run("refuses a code for a number that is already verified", func(t *testing.T) {
		id := bindingUser(t, "binding_set_verified@test.com", bindingPhoneSent, time.Now().Unix())
		err := testDBService.SetPhoneVerificationCode(testInstanceID, id, bindingCode("444444", bindingPhoneSent))
		if !errors.Is(err, ErrPhoneNotPending) {
			t.Errorf("expected ErrPhoneNotPending, got %v", err)
		}
	})

	t.Run("refuses a code that carries no number", func(t *testing.T) {
		id := bindingUser(t, "binding_set_unbound@test.com", bindingPhoneSent, 0)
		err := testDBService.SetPhoneVerificationCode(testInstanceID, id, bindingCode("555555", ""))
		if !errors.Is(err, ErrPhoneNotPending) {
			t.Errorf("expected ErrPhoneNotPending, got %v", err)
		}
	})

	t.Run("refuses a code for a user that does not exist", func(t *testing.T) {
		err := testDBService.SetPhoneVerificationCode(testInstanceID, primitive.NewObjectID().Hex(), bindingCode("666666", bindingPhoneSent))
		if !errors.Is(err, ErrPhoneNotPending) {
			t.Errorf("expected ErrPhoneNotPending, got %v", err)
		}
	})
}

func TestDbFinalizePhoneVerificationConfirmsOnlyTheMatchingNumber(t *testing.T) {
	t.Run("confirms the number the code was sent to", func(t *testing.T) {
		id := bindingUser(t, "binding_finalize_ok@test.com", bindingPhoneSent, 0)
		updated, err := testDBService.FinalizePhoneVerification(testInstanceID, id, bindingPhoneSent)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		phone, found := updated.FindContactInfoByTypeAndAddr(models.ContactTypePhone, bindingPhoneSent)
		if !found || phone.ConfirmedAt == 0 {
			t.Errorf("the number the code was sent to was not confirmed: %+v", phone)
		}
		if !containsChannel(updated.ContactPreferences.PreferredChannels, models.ChannelWhatsApp) {
			t.Errorf("the whatsapp channel was not enabled: %v", updated.ContactPreferences.PreferredChannels)
		}
	})

	t.Run("refuses to confirm a number the code was not sent to", func(t *testing.T) {
		id := bindingUser(t, "binding_finalize_foreign@test.com", bindingPhoneCurrent, 0)

		if _, err := testDBService.FinalizePhoneVerification(testInstanceID, id, bindingPhoneSent); err != mongo.ErrNoDocuments {
			t.Errorf("expected mongo.ErrNoDocuments, got %v", err)
		}

		user, err := testDBService.GetUserByID(testInstanceID, id)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		phone, found := user.FindContactInfoByTypeAndAddr(models.ContactTypePhone, bindingPhoneCurrent)
		if !found {
			t.Fatal("the current number disappeared")
		}
		if phone.ConfirmedAt != 0 {
			t.Errorf("a code sent elsewhere confirmed the current number: %+v", phone)
		}
		if containsChannel(user.ContactPreferences.PreferredChannels, models.ChannelWhatsApp) {
			t.Errorf("the whatsapp channel was enabled for an unproven number: %v", user.ContactPreferences.PreferredChannels)
		}
	})

	t.Run("confirms only the entry it was given, when the account carries more", func(t *testing.T) {
		accountID := "binding_finalize_one_entry@test.com"
		id := addReserveTestUser(t, accountID, []int64{}, []models.ContactInfo{
			{ID: primitive.NewObjectID(), Type: models.ContactTypeEmail, Email: accountID, ConfirmedAt: time.Now().Unix()},
			{ID: primitive.NewObjectID(), Type: models.ContactTypePhone, Phone: bindingPhoneCurrent},
			{ID: primitive.NewObjectID(), Type: models.ContactTypePhone, Phone: bindingPhoneSent},
		})

		updated, err := testDBService.FinalizePhoneVerification(testInstanceID, id, bindingPhoneSent)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		sent, _ := updated.FindContactInfoByTypeAndAddr(models.ContactTypePhone, bindingPhoneSent)
		if sent.ConfirmedAt == 0 {
			t.Errorf("the number the code was sent to was not confirmed: %+v", sent)
		}
		other, _ := updated.FindContactInfoByTypeAndAddr(models.ContactTypePhone, bindingPhoneCurrent)
		if other.ConfirmedAt != 0 {
			t.Errorf("a number the code was not sent to was confirmed too: %+v", other)
		}
	})
}

// The stale-write interleaving the finding describes, committed one DB operation at a time: the
// request read the account while the number was still there, the number was replaced meanwhile,
// and only then does the request store the code it is about to send to the old number.
func TestDbStaleCodeCannotFollowAReplacedNumber(t *testing.T) {
	id := bindingUser(t, "binding_stale_sequence@test.com", bindingPhoneSent, 0)

	if err := testDBService.ReplacePhoneContactInfo(testInstanceID, id, models.ContactInfo{
		ID: primitive.NewObjectID(), Type: models.ContactTypePhone, Phone: bindingPhoneCurrent,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err := testDBService.SetPhoneVerificationCode(testInstanceID, id, bindingCode("777777", bindingPhoneSent))
	if !errors.Is(err, ErrPhoneNotPending) {
		t.Fatalf("the stale code was accepted for a number that is gone: %v", err)
	}

	if _, err := testDBService.FinalizePhoneVerification(testInstanceID, id, bindingPhoneSent); err != mongo.ErrNoDocuments {
		t.Errorf("the stale code confirmed the replacement number: %v", err)
	}
	user, _ := testDBService.GetUserByID(testInstanceID, id)
	phone, _ := user.FindContactInfoByTypeAndAddr(models.ContactTypePhone, bindingPhoneCurrent)
	if phone.ConfirmedAt != 0 {
		t.Errorf("the replacement number was confirmed by a code sent elsewhere: %+v", phone)
	}
}
