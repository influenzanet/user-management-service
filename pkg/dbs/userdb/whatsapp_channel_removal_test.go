package userdb

import (
	"fmt"
	"testing"
	"time"

	"github.com/influenzanet/user-management-service/pkg/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// removalUser creates an account with e-mail and whatsapp enabled for phone; with verified false,
// the number is then left pending verification (whatsapp stays stored).
func removalUser(t *testing.T, accountID, phone string, verified bool) primitive.ObjectID {
	t.Helper()
	id := claimTestUser(t, accountID, phone, time.Now().Unix())
	if _, err := testDBService.UpdateContactPreferences(testInstanceID, id, models.ContactPreferences{
		PreferredChannels: []string{models.ChannelEmail, models.ChannelWhatsApp},
	}); err != nil {
		t.Fatal(err)
	}
	_id, _ := primitive.ObjectIDFromHex(id)
	if !verified {
		ctx, cancel := testDBService.getContext()
		defer cancel()
		if _, err := testDBService.collectionRefUsers(testInstanceID).UpdateOne(ctx,
			bson.M{"_id": _id, "contactInfos.type": models.ContactTypePhone},
			bson.M{"$set": bson.M{"contactInfos.$.confirmedAt": 0}}); err != nil {
			t.Fatal(err)
		}
	}
	return _id
}

func channelsAfterRemoval(t *testing.T, id primitive.ObjectID) []string {
	t.Helper()
	user, err := testDBService.GetUserByID(testInstanceID, id.Hex())
	if err != nil {
		t.Fatal(err)
	}
	return user.ContactPreferences.PreferredChannels
}

// An opt-out sent from a number removes the channel only while the account still holds that
// number verified: whatsapp enabled since for another number stays.
func TestRemoveWhatsAppChannelOnlyForTheNumberStillHeld(t *testing.T) {
	held, other := "+393391000001", "+393391000002"
	id := removalUser(t, "remove_wa_held@test.com", held, true)

	if _, removed, err := testDBService.RemoveWhatsAppChannel(testInstanceID, id, other); err != nil || removed {
		t.Fatalf("removed=%v err=%v for a number the account does not hold", removed, err)
	}
	if got := channelsAfterRemoval(t, id); fmt.Sprint(got) != "[email whatsapp]" {
		t.Fatalf("channels = %v, want them untouched", got)
	}

	before, removed, err := testDBService.RemoveWhatsAppChannel(testInstanceID, id, held)
	if err != nil || !removed {
		t.Fatalf("removed=%v err=%v for the number the account holds", removed, err)
	}
	if fmt.Sprint(before.ContactPreferences.PreferredChannels) != "[email whatsapp]" {
		t.Errorf("before = %v, want the channels as they were", before.ContactPreferences.PreferredChannels)
	}
	if got := channelsAfterRemoval(t, id); fmt.Sprint(got) != "[email]" {
		t.Errorf("channels = %v, want [email]", got)
	}
}

func TestRemoveWhatsAppChannelUnverifiedNumberIsNotHeld(t *testing.T) {
	phone := "+393391000003"
	id := removalUser(t, "remove_wa_unverified@test.com", phone, false)
	if _, removed, err := testDBService.RemoveWhatsAppChannel(testInstanceID, id, phone); err != nil || removed {
		t.Fatalf("removed=%v err=%v for a number only pending verification", removed, err)
	}
	if got := channelsAfterRemoval(t, id); fmt.Sprint(got) != "[email whatsapp]" {
		t.Errorf("channels = %v, want them untouched", got)
	}
}

func TestFindVerifiedPhoneHolder(t *testing.T) {
	t.Run("the owner holds the number verified", func(t *testing.T) {
		phone := "+393391000011"
		owner := claimTestUser(t, "holder_verified@test.com", phone, time.Now().Unix())
		seedClaim(t, phone, owner)
		got, found, err := testDBService.FindVerifiedPhoneHolder(testInstanceID, phone)
		if err != nil || !found || got.Hex() != owner {
			t.Errorf("got %s found=%v err=%v, want %s", got.Hex(), found, err, owner)
		}
	})

	t.Run("no claim", func(t *testing.T) {
		phone := "+393391000012"
		claimTestUser(t, "holder_no_claim@test.com", phone, time.Now().Unix())
		if _, found, err := testDBService.FindVerifiedPhoneHolder(testInstanceID, phone); err != nil || found {
			t.Errorf("found=%v err=%v for a number nobody has claimed", found, err)
		}
	})

	t.Run("a stale claim: the owner has replaced the number", func(t *testing.T) {
		phone := "+393391000013"
		owner := claimTestUser(t, "holder_stale@test.com", "+393391000014", time.Now().Unix())
		seedClaim(t, phone, owner)
		if _, found, err := testDBService.FindVerifiedPhoneHolder(testInstanceID, phone); err != nil || found {
			t.Errorf("found=%v err=%v for a claim whose owner no longer holds the number", found, err)
		}
	})
}
