package userdb

import (
	"testing"
	"time"

	"github.com/influenzanet/user-management-service/pkg/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func claimTestUser(t *testing.T, accountID, phone string, confirmedAt int64) string {
	t.Helper()
	return addReserveTestUser(t, accountID, []int64{}, []models.ContactInfo{
		{ID: primitive.NewObjectID(), Type: models.ContactTypeEmail, Email: accountID, ConfirmedAt: time.Now().Unix()},
		{ID: primitive.NewObjectID(), Type: models.ContactTypePhone, Phone: phone, ConfirmedAt: confirmedAt},
	})
}

func seedClaim(t *testing.T, phone string, owner string) {
	t.Helper()
	ownerID, err := primitive.ObjectIDFromHex(owner)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := testDBService.getContext()
	defer cancel()
	if _, err := testDBService.collectionRefVerifiedPhones(testInstanceID).InsertOne(ctx,
		bson.M{"_id": phone, "userId": ownerID, "claimedAt": time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
}

func claimOwner(t *testing.T, phone string) (string, bool) {
	t.Helper()
	ctx, cancel := testDBService.getContext()
	defer cancel()
	var claim struct {
		UserID primitive.ObjectID `bson:"userId"`
	}
	err := testDBService.collectionRefVerifiedPhones(testInstanceID).FindOne(ctx, bson.M{"_id": phone}).Decode(&claim)
	if err != nil {
		return "", false
	}
	return claim.UserID.Hex(), true
}

func setHook(t *testing.T, hook func(userID primitive.ObjectID)) {
	t.Helper()
	afterPhoneClaimHook = hook
	t.Cleanup(func() { afterPhoneClaimHook = nil })
}

func confirmedPhone(t *testing.T, userID, phone string) bool {
	t.Helper()
	user, err := testDBService.GetUserByID(testInstanceID, userID)
	if err != nil {
		t.Fatal(err)
	}
	ci, found := user.FindContactInfoByTypeAndAddr(models.ContactTypePhone, phone)
	return found && ci.ConfirmedAt > 0
}

// A holds a stale claim of its own on the number (it let the number go earlier), adds it again
// and verifies it while B verifies it too. The interleaving: A finds the claim in its own name
// and goes on without inserting anything; B sees an owner that does not hold the number and takes
// the claim over; A then writes its confirmation. Both would end up verified; the claim read
// after the confirmation makes A give it back.
func TestFinalizePhoneVerificationStaleOwnClaimTakenOverBetweenClaimAndConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		channels []string
	}{
		{"whatsapp was not enabled", nil},
		{"whatsapp was already enabled", []string{models.ChannelEmail, models.ChannelWhatsApp}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			phone := "+3912300020" + map[bool]string{true: "01", false: "02"}[tc.channels == nil]
			a := claimTestUser(t, "stale_own_a_"+phone+"@test.com", phone, 0)
			b := claimTestUser(t, "stale_own_b_"+phone+"@test.com", phone, 0)
			if tc.channels != nil {
				seedChannels(t, a, tc.channels)
			}
			aID, _ := primitive.ObjectIDFromHex(a)
			seedClaim(t, phone, a)

			setHook(t, func(userID primitive.ObjectID) {
				if userID != aID {
					return
				}
				afterPhoneClaimHook = nil // B runs the whole call without the hook
				if _, err := testDBService.FinalizePhoneVerification(testInstanceID, b, phone); err != nil {
					t.Errorf("B could not take over the stale claim: %v", err)
				}
			})

			_, err := testDBService.FinalizePhoneVerification(testInstanceID, a, phone)
			if err != ErrPhoneVerifiedElsewhere {
				t.Fatalf("A was not refused: %v", err)
			}
			if confirmedPhone(t, a, phone) {
				t.Error("A is still confirmed")
			}
			if !confirmedPhone(t, b, phone) {
				t.Error("B lost the number")
			}
			if owner, _ := claimOwner(t, phone); owner != b {
				t.Errorf("the claim names %s, want B", owner)
			}

			userA, _ := testDBService.GetUserByID(testInstanceID, a)
			wantWhatsApp := tc.channels != nil
			if containsChannel(userA.ContactPreferences.PreferredChannels, models.ChannelWhatsApp) != wantWhatsApp {
				t.Errorf("A's channels after the revert: %v", userA.ContactPreferences.PreferredChannels)
			}
			if !containsChannel(userB(t, b).ContactPreferences.PreferredChannels, models.ChannelWhatsApp) {
				t.Error("B was not given the whatsapp channel")
			}
		})
	}
}

func userB(t *testing.T, id string) models.User {
	t.Helper()
	user, err := testDBService.GetUserByID(testInstanceID, id)
	if err != nil {
		t.Fatal(err)
	}
	return user
}

// The revert is conditional on the confirmation it made: a confirmation written by somebody else
// in the meantime (a different confirmedAt) is not touched.
func TestRevertPhoneConfirmationLeavesAnotherConfirmationAlone(t *testing.T) {
	const phone = "+391230002003"
	a := claimTestUser(t, "revert_other@test.com", phone, 1234)
	aID, _ := primitive.ObjectIDFromHex(a)
	if err := testDBService.revertPhoneConfirmation(testInstanceID, aID, phone, 999, false); err != nil {
		t.Fatal(err)
	}
	if !confirmedPhone(t, a, phone) {
		t.Error("a confirmation that was not written by the reverting call was removed")
	}
}

// A repeated verification by the holder must not release the holder's claim when the
// confirmation write fails (here the number disappears between the claim and the confirmation).
func TestFinalizePhoneVerificationFailureKeepsAClaimTheCallDidNotCreate(t *testing.T) {
	const phone = "+391230002004"
	a := claimTestUser(t, "keep_claim_a@test.com", phone, 0)
	aID, _ := primitive.ObjectIDFromHex(a)
	if _, err := testDBService.FinalizePhoneVerification(testInstanceID, a, phone); err != nil {
		t.Fatal(err)
	}

	setHook(t, func(userID primitive.ObjectID) {
		afterPhoneClaimHook = nil
		if _, err := testDBService.DeletePhoneNumber(testInstanceID, a); err != nil {
			t.Errorf("setup failed: %v", err)
		}
	})
	if _, err := testDBService.FinalizePhoneVerification(testInstanceID, a, phone); err == nil {
		t.Fatal("expected the confirmation to fail")
	}
	if owner, ok := claimOwner(t, phone); !ok || owner != aID.Hex() {
		t.Errorf("the holder's claim was released by a call that did not create it: %q %v", owner, ok)
	}
}

// A claim the failing call itself put in place is still released.
func TestFinalizePhoneVerificationFailureReleasesTheClaimItCreated(t *testing.T) {
	const phone = "+391230002005"
	a := claimTestUser(t, "release_claim_a@test.com", phone, 0)

	setHook(t, func(userID primitive.ObjectID) {
		afterPhoneClaimHook = nil
		if _, err := testDBService.DeletePhoneNumber(testInstanceID, a); err != nil {
			t.Errorf("setup failed: %v", err)
		}
	})
	if _, err := testDBService.FinalizePhoneVerification(testInstanceID, a, phone); err == nil {
		t.Fatal("expected the confirmation to fail")
	}
	if _, ok := claimOwner(t, phone); ok {
		t.Error("the claim created by the failed call was left behind")
	}
}

// The other half of the race: B finds the owner on record (A) not verified, A confirms, and only
// then B takes the claim over. A's own check of the claim ran before the takeover and passed, so
// B must notice that A has meanwhile confirmed, and hand the claim back.
func TestFinalizePhoneVerificationOwnerConfirmsBetweenStaleCheckAndTakeover(t *testing.T) {
	const phone = "+391230002006"
	a := claimTestUser(t, "takeover_race_a@test.com", phone, 0)
	b := claimTestUser(t, "takeover_race_b@test.com", phone, 0)
	seedClaim(t, phone, a)

	afterStaleOwnerCheckHook = func(claimant primitive.ObjectID) {
		afterStaleOwnerCheckHook = nil
		if _, err := testDBService.FinalizePhoneVerification(testInstanceID, a, phone); err != nil {
			t.Errorf("A could not confirm: %v", err)
		}
	}
	t.Cleanup(func() { afterStaleOwnerCheckHook = nil })

	if _, err := testDBService.FinalizePhoneVerification(testInstanceID, b, phone); err != ErrPhoneVerifiedElsewhere {
		t.Fatalf("B was not refused: %v", err)
	}
	if !confirmedPhone(t, a, phone) || confirmedPhone(t, b, phone) {
		t.Errorf("A confirmed=%v B confirmed=%v", confirmedPhone(t, a, phone), confirmedPhone(t, b, phone))
	}
	if owner, _ := claimOwner(t, phone); owner != a {
		t.Errorf("the claim names %s, want A %s", owner, a)
	}
}
