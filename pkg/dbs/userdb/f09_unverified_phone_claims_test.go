package userdb

import (
	"context"
	"fmt"
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

func TestDbReleaseUnverifiedPhoneClaims(t *testing.T) {
	t.Run("removes another account's unverified claim and the code bound to it", func(t *testing.T) {
		// Its own number: a shared number would make the count below depend on the order of
		// the tests.
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
		if fmt.Sprint(released) != "[email]" {
			t.Errorf("a released account must lose whatsapp and keep email: %v", released)
		}

		kept := channelsOfUser(t, verifier)
		if !hasChan(kept, models.ChannelWhatsApp) || !hasChan(kept, models.ChannelEmail) {
			t.Errorf("the verifying account's channels were disturbed: %v", kept)
		}
	})

	// The claim takeover never adds e-mail. An account reached on whatsapp only is left with
	// no channel; an account with no channel stays without one.
	for name, channels := range map[string][]string{
		"whatsapp only": {models.ChannelWhatsApp},
		"no channel":    {},
	} {
		t.Run("a released account with "+name+" gains no e-mail", func(t *testing.T) {
			phone := "+39123000101" + fmt.Sprint(3+len(channels))
			stale := claimUser(t, "release_noemail_"+fmt.Sprint(len(channels))+"@test.com", phone, 0, "777777")
			verifier := claimUser(t, "release_noemail_verifier_"+fmt.Sprint(len(channels))+"@test.com", phone, time.Now().Unix(), "")
			seedChannels(t, stale, channels)

			if _, err := testDBService.ReleaseUnverifiedPhoneClaims(testInstanceID, phone, verifier); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got := channelsOfUser(t, stale)
			if got == nil || len(got) != 0 {
				t.Errorf("expected no channel, got %#v", got)
			}
			if phones := phoneContactsOf(t, stale); len(phones) != 0 {
				t.Errorf("the claim was not released: %+v", phones)
			}
		})
	}

	t.Run("a legacy account with no channel list still reads as e-mail", func(t *testing.T) {
		const phone = "+391230001015"
		stale := claimUser(t, "release_legacy_stale@test.com", phone, 0, "")
		verifier := claimUser(t, "release_legacy_verifier@test.com", phone, time.Now().Unix(), "")
		staleID, _ := primitive.ObjectIDFromHex(stale)
		if _, err := testDBService.collectionRefUsers(testInstanceID).UpdateOne(context.Background(),
			bson.M{"_id": staleID}, bson.M{"$unset": bson.M{"contactPreferences.preferredChannels": ""}}); err != nil {
			t.Fatal(err)
		}

		if _, err := testDBService.ReleaseUnverifiedPhoneClaims(testInstanceID, phone, verifier); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		user, _ := testDBService.GetUserByID(testInstanceID, stale)
		if got := fmt.Sprint(user.ContactPreferences.EffectivePreferredChannels()); got != "[email]" {
			t.Errorf("a legacy account must keep its e-mail default, got %s", got)
		}
	})

	t.Run("a verified contact on another account is never removed", func(t *testing.T) {
		const phone = "+391230001016"
		// Two accounts both verified (data written before claims existed): the release removes
		// claims only, never proven contacts, so the other verified one stays.
		other := claimUser(t, "release_keep_verified_other@test.com", phone, time.Now().Unix(), "")
		verifier := claimUser(t, "release_keep_verified_verifier@test.com", phone, time.Now().Unix(), "")
		seedChannels(t, other, []string{models.ChannelWhatsApp})

		released, err := testDBService.ReleaseUnverifiedPhoneClaims(testInstanceID, phone, verifier)
		if err != nil {
			t.Fatal(err)
		}
		if released != 0 {
			t.Errorf("a verified contact was counted as released: %d", released)
		}
		if phones := phoneContactsOf(t, other); len(phones) != 1 || phones[0].ConfirmedAt == 0 {
			t.Errorf("a verified contact was removed: %+v", phones)
		}
		if got := fmt.Sprint(channelsOfUser(t, other)); got != "[whatsapp]" {
			t.Errorf("the channels of an untouched account changed: %s", got)
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
