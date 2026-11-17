package userdb

import (
	"testing"
	"time"

	"github.com/influenzanet/user-management-service/pkg/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Item 48: a claim in verifiedPhones names the account a number belongs to. Once the number is
// deleted or replaced, or the account deleted, nothing should keep that: the claim goes too.

// verifiedClaimUser creates an account holding the number verified, with the claim that a real
// verification leaves, and returns its id.
func verifiedClaimUser(t *testing.T, accountID, phone string) string {
	t.Helper()
	id := claimTestUser(t, accountID, phone, 0)
	if _, err := testDBService.FinalizePhoneVerification(testInstanceID, id, phone); err != nil {
		t.Fatalf("failed to verify the number: %v", err)
	}
	if owner, ok := claimOwner(t, phone); !ok || owner != id {
		t.Fatalf("setup: the verification left no claim (%q, %v)", owner, ok)
	}
	return id
}

func TestDbPhoneDeletionReleasesTheClaim(t *testing.T) {
	t.Run("DeletePhoneNumber", func(t *testing.T) {
		const phone = "+391230004801"
		id := verifiedClaimUser(t, "claim_release_delete@test.com", phone)
		if _, err := testDBService.DeletePhoneNumber(testInstanceID, id); err != nil {
			t.Fatal(err)
		}
		if owner, ok := claimOwner(t, phone); ok {
			t.Errorf("the claim survived the deletion of the number (owner %s)", owner)
		}
	})

	t.Run("ReplacePhoneContactInfo releases the number it replaces", func(t *testing.T) {
		const phone = "+391230004802"
		id := verifiedClaimUser(t, "claim_release_replace@test.com", phone)
		if err := testDBService.ReplacePhoneContactInfo(testInstanceID, id, models.ContactInfo{
			ID: primitive.NewObjectID(), Type: models.ContactTypePhone, Phone: "+391230004803",
		}); err != nil {
			t.Fatal(err)
		}
		if owner, ok := claimOwner(t, phone); ok {
			t.Errorf("the claim on the replaced number survived (owner %s)", owner)
		}
	})

	t.Run("DeleteUser", func(t *testing.T) {
		const phone = "+391230004804"
		id := verifiedClaimUser(t, "claim_release_user@test.com", phone)
		if err := testDBService.DeleteUser(testInstanceID, id); err != nil {
			t.Fatal(err)
		}
		if owner, ok := claimOwner(t, phone); ok {
			t.Errorf("the claim survived the deletion of the account (owner %s)", owner)
		}
	})

	// The removal after three wrong codes goes through DeletePhoneNumber (VerifyWhatsAppCode), so
	// it releases the same way; a claim left by an earlier failure is swept on the same call.
	t.Run("a claim left behind earlier is released on the next deletion", func(t *testing.T) {
		const phone = "+391230004805"
		id := claimTestUser(t, "claim_release_leftover@test.com", "+391230004806", 0)
		seedClaim(t, phone, id) // a claim on a number the account no longer carries
		if _, err := testDBService.DeletePhoneNumber(testInstanceID, id); err != nil {
			t.Fatal(err)
		}
		if _, ok := claimOwner(t, phone); ok {
			t.Error("a leftover claim of the account survived")
		}
	})
}

// What the release must not touch.
func TestDbClaimReleaseLeavesOtherClaimsAlone(t *testing.T) {
	t.Run("another account's claim on another number", func(t *testing.T) {
		other := verifiedClaimUser(t, "claim_keep_other@test.com", "+391230004811")
		id := verifiedClaimUser(t, "claim_keep_self@test.com", "+391230004812")
		if _, err := testDBService.DeletePhoneNumber(testInstanceID, id); err != nil {
			t.Fatal(err)
		}
		if owner, ok := claimOwner(t, "+391230004811"); !ok || owner != other {
			t.Errorf("another account's claim was released (%q, %v)", owner, ok)
		}
	})

	t.Run("a claim that has passed to another account", func(t *testing.T) {
		// A deleted the number; B verified it and holds the claim. A deleting again (or A being
		// deleted) must not take B's claim with it: the release names the owner.
		const phone = "+391230004813"
		a := verifiedClaimUser(t, "claim_passed_a@test.com", phone)
		if _, err := testDBService.DeletePhoneNumber(testInstanceID, a); err != nil {
			t.Fatal(err)
		}
		b := verifiedClaimUser(t, "claim_passed_b@test.com", phone)
		if err := testDBService.DeleteUser(testInstanceID, a); err != nil {
			t.Fatal(err)
		}
		if owner, ok := claimOwner(t, phone); !ok || owner != b {
			t.Errorf("B's claim was released by A (%q, %v)", owner, ok)
		}
	})

	t.Run("the same account's claim on a number it still holds verified", func(t *testing.T) {
		// Data written before numbers were normalised can leave an account with two phone
		// entries; replacing (or deleting) never touches a claim the account still holds.
		const phone = "+391230004814"
		id := verifiedClaimUser(t, "claim_still_held@test.com", phone)
		if err := testDBService.releaseClaimsNoLongerHeld(testInstanceID, mustObjectID(t, id)); err != nil {
			t.Fatal(err)
		}
		if owner, ok := claimOwner(t, phone); !ok || owner != id {
			t.Errorf("a claim the account still holds was released (%q, %v)", owner, ok)
		}
	})

	t.Run("an owner that verifies again between the check and the delete keeps it", func(t *testing.T) {
		// The delete names the revision read before the check; confirming the claim moves it.
		const phone = "+391230004815"
		id := verifiedClaimUser(t, "claim_reverify@test.com", phone)
		idOID := mustObjectID(t, id)
		// Make the number unverified on the account (as a phone change in flight would), so the
		// release finds the claim stale, then let the owner verify again inside the window.
		ctx, cancel := testDBService.getContext()
		_, err := testDBService.collectionRefUsers(testInstanceID).UpdateOne(ctx,
			bson.M{"_id": idOID, "contactInfos.type": models.ContactTypePhone},
			bson.M{"$set": bson.M{"contactInfos.$.confirmedAt": 0}})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		afterHeldCheckHook = func(owner primitive.ObjectID) {
			afterHeldCheckHook = nil
			if _, err := testDBService.FinalizePhoneVerification(testInstanceID, id, phone); err != nil {
				t.Errorf("re-verification failed: %v", err)
			}
		}
		t.Cleanup(func() { afterHeldCheckHook = nil })

		if err := testDBService.releaseClaimsNoLongerHeld(testInstanceID, idOID); err != nil {
			t.Fatal(err)
		}
		if owner, ok := claimOwner(t, phone); !ok || owner != id {
			t.Errorf("a claim confirmed again inside the window was deleted (%q, %v)", owner, ok)
		}
		if !confirmedPhone(t, id, phone) {
			t.Error("the re-verification was undone")
		}
	})
}

// The sweep for what was left behind, used by tools/normalize-phone-numbers.
func TestDbReleaseStaleVerifiedPhoneClaims(t *testing.T) {
	const (
		validPhone   = "+391230004821"
		deletedPhone = "+391230004822"
		goneOwner    = "+391230004823"
	)
	valid := verifiedClaimUser(t, "sweep_valid@test.com", validPhone)
	unverified := claimTestUser(t, "sweep_unverified@test.com", "+391230004824", 0)
	seedClaim(t, deletedPhone, unverified)                 // owner exists, does not hold the number
	seedClaim(t, goneOwner, primitive.NewObjectID().Hex()) // owner does not exist

	staleOf := func(list []StaleVerifiedPhoneClaim) map[string]string {
		m := map[string]string{}
		for _, c := range list {
			m[c.Number] = c.UserID
		}
		return m
	}

	t.Run("a dry run reports and writes nothing", func(t *testing.T) {
		stale, released, err := testDBService.ReleaseStaleVerifiedPhoneClaims(testInstanceID, false)
		if err != nil {
			t.Fatal(err)
		}
		found := staleOf(stale)
		if found[deletedPhone] != unverified {
			t.Errorf("the claim of an account that no longer holds the number was not reported: %v", found)
		}
		if _, ok := found[goneOwner]; !ok {
			t.Errorf("the claim of a deleted account was not reported: %v", found)
		}
		if _, ok := found[validPhone]; ok {
			t.Errorf("a valid claim was reported stale: %v", found)
		}
		if released != 0 {
			t.Errorf("a dry run released %d claims", released)
		}
		for _, phone := range []string{deletedPhone, goneOwner, validPhone} {
			if _, ok := claimOwner(t, phone); !ok {
				t.Errorf("a dry run deleted the claim on %s", phone)
			}
		}
	})

	t.Run("apply releases the stale claims only", func(t *testing.T) {
		stale, released, err := testDBService.ReleaseStaleVerifiedPhoneClaims(testInstanceID, true)
		if err != nil {
			t.Fatal(err)
		}
		found := staleOf(stale)
		if _, ok := found[deletedPhone]; !ok {
			t.Errorf("not released: %s (%v)", deletedPhone, found)
		}
		if _, ok := found[goneOwner]; !ok {
			t.Errorf("not released: %s (%v)", goneOwner, found)
		}
		if released != len(stale) {
			t.Errorf("released %d, reported %d", released, len(stale))
		}
		for _, phone := range []string{deletedPhone, goneOwner} {
			if _, ok := claimOwner(t, phone); ok {
				t.Errorf("the stale claim on %s is still there", phone)
			}
		}
		if owner, ok := claimOwner(t, validPhone); !ok || owner != valid {
			t.Errorf("a valid claim was released (%q, %v)", owner, ok)
		}
	})

	t.Run("a second run finds nothing", func(t *testing.T) {
		stale, _, err := testDBService.ReleaseStaleVerifiedPhoneClaims(testInstanceID, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range stale {
			if c.Number == deletedPhone || c.Number == goneOwner || c.Number == validPhone {
				t.Errorf("found again: %+v", c)
			}
		}
	})
}

func mustObjectID(t *testing.T, hex string) primitive.ObjectID {
	t.Helper()
	id, err := primitive.ObjectIDFromHex(hex)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// Nothing asks whether a number is taken before the code is sent, so verification is where a
// number held by another account is refused. Accounts verified before claims existed have no claim until the
// migration seeds one; they are refused all the same.
func TestDbFinalizePhoneVerificationRefusesANumberVerifiedElsewhereWithoutAClaim(t *testing.T) {
	const phone = "+393476124831"                                                   // a valid number: its other spelling must normalise to it
	holder := claimTestUser(t, "noclaim_holder@test.com", phone, time.Now().Unix()) // verified, no claim
	if _, ok := claimOwner(t, phone); ok {
		t.Fatal("setup: the holder must have no claim")
	}
	// The second account carries the number in another spelling: stored data may predate E.164.
	other := claimTestUser(t, "noclaim_other@test.com", "0039 347 612 4831", 0)

	if _, err := testDBService.FinalizePhoneVerification(testInstanceID, other, "0039 347 612 4831"); err != ErrPhoneVerifiedElsewhere {
		t.Fatalf("expected ErrPhoneVerifiedElsewhere, got %v", err)
	}
	if confirmedPhone(t, other, "0039 347 612 4831") {
		t.Error("the second account was confirmed")
	}
	if owner, ok := claimOwner(t, phone); ok {
		t.Errorf("the refused verification left a claim (owner %s)", owner)
	}
	if !confirmedPhone(t, holder, phone) {
		t.Error("the holder lost the number")
	}
	// The holder itself still verifies (idempotently) and gets its claim.
	if _, err := testDBService.FinalizePhoneVerification(testInstanceID, holder, phone); err != nil {
		t.Fatalf("the holder could not verify its own number: %v", err)
	}
	if owner, ok := claimOwner(t, phone); !ok || owner != holder {
		t.Errorf("the holder's verification did not claim the number (%q, %v)", owner, ok)
	}
}
