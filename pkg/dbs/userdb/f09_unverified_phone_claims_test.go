package userdb

import (
	"context"
	"testing"
	"time"

	"github.com/influenzanet/user-management-service/pkg/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// F-09: a number was counted as taken as soon as any account carried it, verified or not. A
// typo in someone else's signup, or a signup abandoned before the code was ever used, therefore
// held the number for good: the real owner could never register it, and no endpoint existed to
// let it go. Only a verified contact proves a number belongs to an account, so only a verified
// contact may hold it, and the moment somebody does prove control of a number every unverified
// claim to it elsewhere is released.

const (
	claimedPhone   = "+391230001001" // the number two accounts reach for
	unrelatedPhone = "+391230001002" // a number nobody contends
)

// claimUser creates a user that carries the number with the given confirmation state, and binds
// a phone verification code to it when the number is still pending, exactly as a real pending
// signup would leave it.
func claimUser(t *testing.T, accountID string, phone string, confirmedAt int64, code string) string {
	t.Helper()
	contacts := []models.ContactInfo{
		{ID: primitive.NewObjectID(), Type: models.ContactTypeEmail, Email: accountID, ConfirmedAt: time.Now().Unix()},
	}
	if phone != "" {
		contacts = append(contacts, models.ContactInfo{
			ID: primitive.NewObjectID(), Type: models.ContactTypePhone, Phone: phone, ConfirmedAt: confirmedAt,
		})
	}
	id := addReserveTestUser(t, accountID, []int64{}, contacts)

	if code != "" {
		now := time.Now().Unix()
		stored := models.VerificationCode{Code: code, CreatedAt: now, ExpiresAt: now + 300, Phone: phone}
		if err := testDBService.SetPhoneVerificationCode(testInstanceID, id, stored); err != nil {
			t.Fatalf("failed to seed the pending code: %v", err)
		}
	}
	return id
}

// seedChannels writes the notification channels directly, so a test can start from an account
// that already had whatsapp enabled for the number it is about to lose.
func seedChannels(t *testing.T, userID string, channels []string) {
	t.Helper()
	user, err := testDBService.GetUserByID(testInstanceID, userID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Written directly rather than through UpdateUser: the channel list is not in that method's
	// allowlist of updatable fields, and a fixture should not need it to be.
	if _, err := testDBService.collectionRefUsers(testInstanceID).UpdateOne(
		context.Background(),
		bson.M{"_id": user.ID},
		bson.M{"$set": bson.M{"contactPreferences.preferredChannels": channels}},
	); err != nil {
		t.Fatalf("failed to seed channels: %v", err)
	}
}

func channelsOfUser(t *testing.T, userID string) []string {
	t.Helper()
	user, err := testDBService.GetUserByID(testInstanceID, userID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return user.ContactPreferences.PreferredChannels
}

func hasChan(channels []string, want string) bool {
	for _, c := range channels {
		if c == want {
			return true
		}
	}
	return false
}

func phoneContactsOf(t *testing.T, userID string) []models.ContactInfo {
	t.Helper()
	user, err := testDBService.GetUserByID(testInstanceID, userID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	phones := []models.ContactInfo{}
	for _, ci := range user.ContactInfos {
		if ci.Type == models.ContactTypePhone {
			phones = append(phones, ci)
		}
	}
	return phones
}

func TestDbIsPhoneNumberTakenCountsOnlyVerified(t *testing.T) {
	ctx := context.Background()

	t.Run("a number only claimed, never verified, is free", func(t *testing.T) {
		claimUser(t, "claims_unverified_holder@test.com", claimedPhone, 0, "")

		taken, err := testDBService.IsPhoneNumberTaken(ctx, testInstanceID, []string{claimedPhone})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if taken {
			t.Error("an unverified claim still blocks the number for its real owner")
		}
	})

	t.Run("a verified number is taken", func(t *testing.T) {
		claimUser(t, "claims_verified_holder@test.com", unrelatedPhone, time.Now().Unix(), "")

		taken, err := testDBService.IsPhoneNumberTaken(ctx, testInstanceID, []string{unrelatedPhone})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !taken {
			t.Error("a number somebody proved control of is reported as free")
		}
	})

	t.Run("the excluding form applies the same rule", func(t *testing.T) {
		const phone = "+391230001003"
		holder := claimUser(t, "claims_excl_holder@test.com", phone, time.Now().Unix(), "")
		other := claimUser(t, "claims_excl_other@test.com", "", 0, "")

		taken, err := testDBService.IsPhoneNumberTakenExcludingUser(ctx, testInstanceID, []string{phone}, other)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !taken {
			t.Error("a verified number held by somebody else is reported as free")
		}

		taken, err = testDBService.IsPhoneNumberTakenExcludingUser(ctx, testInstanceID, []string{phone}, holder)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if taken {
			t.Error("the holder is told its own number is taken")
		}
	})
}

func TestDbReleaseUnverifiedPhoneClaims(t *testing.T) {
	t.Run("removes another account's unverified claim and the code bound to it", func(t *testing.T) {
		// Its own number: the uniqueness test above leaves a claim on claimedPhone, and a
		// shared number would make the count below depend on the order of the tests.
		const phone = "+391230001009"
		stale := claimUser(t, "release_stale@test.com", phone, 0, "111111")
		verifier := claimUser(t, "release_verifier@test.com", phone, time.Now().Unix(), "")

		released, err := testDBService.ReleaseUnverifiedPhoneClaims(testInstanceID, phone, verifier)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if released != 1 {
			t.Errorf("expected 1 released claim, got %d", released)
		}

		if phones := phoneContactsOf(t, stale); len(phones) != 0 {
			t.Errorf("the stale claim survived: %+v", phones)
		}
		if code := storedCode(t, stale); code.Code != "" || code.Phone != "" {
			t.Errorf("the code bound to the released number was kept: %+v", code)
		}
	})

	t.Run("leaves the account that proved control untouched", func(t *testing.T) {
		const phone = "+391230001004"
		verifier := claimUser(t, "release_keeps_verifier@test.com", phone, time.Now().Unix(), "")

		if _, err := testDBService.ReleaseUnverifiedPhoneClaims(testInstanceID, phone, verifier); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		phones := phoneContactsOf(t, verifier)
		if len(phones) != 1 || phones[0].Phone != phone || phones[0].ConfirmedAt <= 0 {
			t.Errorf("the verifier lost the number it had just proved: %+v", phones)
		}
	})

	t.Run("leaves a verified claim on the same number alone", func(t *testing.T) {
		const phone = "+391230001005"
		// Two accounts with the number verified should not exist, but if one ever did this
		// method must not be the thing that silently deletes a proven contact.
		otherVerified := claimUser(t, "release_other_verified@test.com", phone, time.Now().Unix(), "")
		verifier := claimUser(t, "release_second_verifier@test.com", phone, time.Now().Unix(), "")

		released, err := testDBService.ReleaseUnverifiedPhoneClaims(testInstanceID, phone, verifier)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if released != 0 {
			t.Errorf("a verified contact was released: %d", released)
		}
		if phones := phoneContactsOf(t, otherVerified); len(phones) != 1 {
			t.Errorf("a verified contact was removed: %+v", phones)
		}
	})

	t.Run("leaves claims on other numbers alone", func(t *testing.T) {
		const phone = "+391230001006"
		const bystanderPhone = "+391230001007"
		bystander := claimUser(t, "release_bystander@test.com", bystanderPhone, 0, "222222")
		verifier := claimUser(t, "release_unrelated_verifier@test.com", phone, time.Now().Unix(), "")

		if _, err := testDBService.ReleaseUnverifiedPhoneClaims(testInstanceID, phone, verifier); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if phones := phoneContactsOf(t, bystander); len(phones) != 1 || phones[0].Phone != bystanderPhone {
			t.Errorf("a claim on an unrelated number was released: %+v", phones)
		}
		if code := storedCode(t, bystander); code.Code != "222222" {
			t.Errorf("a code for an unrelated number was reset: %+v", code)
		}
	})

	t.Run("a released account loses whatsapp and keeps email", func(t *testing.T) {
		// Otherwise the account is left with whatsapp enabled and no number to deliver it to.
		const phone = "+391230001011"
		stale := claimUser(t, "release_channels_stale@test.com", phone, 0, "666666")
		verifier := claimUser(t, "release_channels_verifier@test.com", phone, time.Now().Unix(), "")
		seedChannels(t, stale, []string{models.ChannelEmail, models.ChannelWhatsApp})
		seedChannels(t, verifier, []string{models.ChannelEmail, models.ChannelWhatsApp})

		if _, err := testDBService.ReleaseUnverifiedPhoneClaims(testInstanceID, phone, verifier); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		released := channelsOfUser(t, stale)
		if hasChan(released, models.ChannelWhatsApp) {
			t.Errorf("a released account kept whatsapp with no phone: %v", released)
		}
		if !hasChan(released, models.ChannelEmail) {
			t.Errorf("a released account was left without email: %v", released)
		}

		kept := channelsOfUser(t, verifier)
		if !hasChan(kept, models.ChannelWhatsApp) || !hasChan(kept, models.ChannelEmail) {
			t.Errorf("the verifying account's channels were disturbed: %v", kept)
		}
	})

	t.Run("an account with no channels gains email when released", func(t *testing.T) {
		const phone = "+391230001012"
		stale := claimUser(t, "release_nochannels_stale@test.com", phone, 0, "777777")
		verifier := claimUser(t, "release_nochannels_verifier@test.com", phone, time.Now().Unix(), "")

		if _, err := testDBService.ReleaseUnverifiedPhoneClaims(testInstanceID, phone, verifier); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !hasChan(channelsOfUser(t, stale), models.ChannelEmail) {
			t.Errorf("a released account was left with no channel at all: %v", channelsOfUser(t, stale))
		}
	})

	t.Run("running it again is a no-op, not an error", func(t *testing.T) {
		// VerifyWhatsAppCode can be reached twice for the same number (a retry, a replayed
		// request), so the second release must simply find nothing left to do.
		const phone = "+391230001010"
		claimUser(t, "release_idempotent_stale@test.com", phone, 0, "555555")
		verifier := claimUser(t, "release_idempotent_verifier@test.com", phone, time.Now().Unix(), "")

		first, err := testDBService.ReleaseUnverifiedPhoneClaims(testInstanceID, phone, verifier)
		if err != nil || first != 1 {
			t.Fatalf("first release: got %d, %v", first, err)
		}
		second, err := testDBService.ReleaseUnverifiedPhoneClaims(testInstanceID, phone, verifier)
		if err != nil {
			t.Errorf("the second release reported an error: %v", err)
		}
		if second != 0 {
			t.Errorf("the second release claimed to have done something: %d", second)
		}
	})

	t.Run("releases every stale claim, not just the first", func(t *testing.T) {
		const phone = "+391230001008"
		first := claimUser(t, "release_many_a@test.com", phone, 0, "333333")
		second := claimUser(t, "release_many_b@test.com", phone, 0, "444444")
		verifier := claimUser(t, "release_many_verifier@test.com", phone, time.Now().Unix(), "")

		released, err := testDBService.ReleaseUnverifiedPhoneClaims(testInstanceID, phone, verifier)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if released != 2 {
			t.Errorf("expected 2 released claims, got %d", released)
		}
		for _, id := range []string{first, second} {
			if phones := phoneContactsOf(t, id); len(phones) != 0 {
				t.Errorf("a stale claim survived: %+v", phones)
			}
		}
	})
}
