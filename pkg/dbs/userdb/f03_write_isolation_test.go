package userdb

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/influenzanet/user-management-service/pkg/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

var f03UserFields = []string{
	"profiles", "roles", "account.accountID", "account.accountConfirmedAt", "account.verificationCode",
	"account.failedLoginAttempts", "account.passwordResetTriggers", "timestamps.lastLogin",
	"timestamps.lastTokenRefresh", "timestamps.markedForDeletion", "contactInfos",
	"contactPreferences.sendNewsletterTo", "contactPreferences.subscribedToNewsletter",
	"contactPreferences.subscribedToWeekly", "contactPreferences.receiveWeeklyMessageDayOfWeek",
}

// Reading a snapshot, committing the phone operation, then saving the snapshot is
// the lost-update interleaving. No scheduler timing or sleeps are needed.
func TestF03StaleUserDoesNotOverwritePhoneState(t *testing.T) {
	for _, operation := range []string{"reserve", "code", "attempts", "add", "replace", "confirm", "delete"} {
		t.Run(operation, func(t *testing.T) {
			user := models.User{
				Account: models.Account{
					AccountID:                 primitive.NewObjectID().Hex() + "@test.com",
					PhoneVerificationCode:     models.VerificationCode{Code: "123456", ExpiresAt: time.Now().Unix() + 300},
					PhoneVerificationAttempts: []int64{},
				},
				ContactPreferences: models.ContactPreferences{PreferredChannels: []string{models.ChannelEmail}},
			}
			user.AddNewEmail(user.Account.AccountID, true)
			if operation != "add" {
				user.AddNewPhone("+391234567890", false)
			}
			id, err := testDBService.AddUser(testInstanceID, user)
			if err != nil {
				t.Fatal(err)
			}
			stale, err := testDBService.GetUserByID(testInstanceID, id)
			if err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "reserve":
				var allowed bool
				allowed, err = testDBService.ReservePhoneVerificationSlot(testInstanceID, id, 3, 300)
				if !allowed {
					t.Fatal("reservation was not accepted")
				}
			case "code":
				err = testDBService.SetPhoneVerificationCode(testInstanceID, id, models.VerificationCode{Code: "654321", Attempts: 2})
			case "attempts":
				for i := 0; i < 3; i++ {
					_, err = testDBService.IncrementVerificationCodeAttempts(testInstanceID, id, 3)
					if err != nil {
						t.Fatal(err)
					}
				}
			case "add":
				var added bool
				added, err = testDBService.AddPhoneContactInfoIfAbsent(testInstanceID, id, models.ContactInfo{ID: primitive.NewObjectID(), Type: models.ContactTypePhone, Phone: "+391234567891"})
				if !added {
					t.Fatal("phone was not added")
				}
			case "replace":
				err = testDBService.ReplacePhoneContactInfo(testInstanceID, id, models.ContactInfo{ID: primitive.NewObjectID(), Type: models.ContactTypePhone, Phone: "+391234567892"})
			case "confirm":
				_, err = testDBService.FinalizePhoneVerification(testInstanceID, id)
			case "delete":
				_, err = testDBService.DeletePhoneNumber(testInstanceID, id)
			}
			if err != nil {
				t.Fatal(err)
			}
			want, err := testDBService.GetUserByID(testInstanceID, id)
			if err != nil {
				t.Fatal(err)
			}
			stale.Profiles = []models.Profile{{ID: primitive.NewObjectID(), Alias: "$literal-profile"}}
			for _, field := range f03UserFields {
				t.Run(field, func(t *testing.T) {
					got, err := testDBService.UpdateUser(testInstanceID, stale, field)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(got.Profiles, stale.Profiles) {
						t.Error("intended profile edit was lost")
					}
					if !reflect.DeepEqual(got.Account.PhoneVerificationAttempts, want.Account.PhoneVerificationAttempts) ||
						got.Account.PhoneVerificationCode != want.Account.PhoneVerificationCode ||
						!reflect.DeepEqual(got.ContactInfos, want.ContactInfos) ||
						!reflect.DeepEqual(got.ContactPreferences.PreferredChannels, want.ContactPreferences.PreferredChannels) {
						t.Errorf("stale %s save overwrote the committed %s operation", field, operation)
					}
				})
			}
		})
	}
}

func TestF03UpdateUserRejectsBroadAndProtectedFields(t *testing.T) {
	for _, field := range []string{"", "account", "timestamps", "contactPreferences", "account.password",
		"account.phoneVerificationCode", "account.phoneVerificationAttempts", "contactPreferences.preferredChannels", "unknown"} {
		if _, err := testDBService.UpdateUser(testInstanceID, models.User{}, field); err == nil {
			t.Errorf("unsafe field %q was accepted", field)
		}
	}
}

func TestF03LegacyPhoneFieldsStayMissingOrNull(t *testing.T) {
	ctx, cancel := testDBService.getContext()
	defer cancel()
	for _, operation := range []string{"$unset", "$set"} {
		t.Run(operation, func(t *testing.T) {
			id, err := testDBService.AddUser(testInstanceID, models.User{Account: models.Account{AccountID: primitive.NewObjectID().Hex()}})
			if err != nil {
				t.Fatal(err)
			}
			user, err := testDBService.GetUserByID(testInstanceID, id)
			if err != nil {
				t.Fatal(err)
			}
			filter := bson.M{"_id": user.ID}
			coll := testDBService.collectionRefUsers(testInstanceID)
			_, err = coll.UpdateOne(ctx, filter, bson.M{operation: bson.M{
				"account.phoneVerificationCode": nil, "account.phoneVerificationAttempts": nil,
				"contactPreferences.preferredChannels": nil,
			}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = coll.UpdateOne(ctx, filter, bson.M{"$set": bson.M{"futureMetadata": "must survive"}})
			if err != nil {
				t.Fatal(err)
			}
			before, err := coll.FindOne(ctx, filter).DecodeBytes()
			if err != nil {
				t.Fatal(err)
			}
			for _, field := range f03UserFields {
				if _, err := testDBService.UpdateUser(testInstanceID, user, field); err != nil {
					t.Fatal(err)
				}
			}
			after, err := coll.FindOne(ctx, filter).DecodeBytes()
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range [][]string{{"account", "phoneVerificationCode"}, {"account", "phoneVerificationAttempts"}, {"contactPreferences", "preferredChannels"}, {"futureMetadata"}} {
				if !reflect.DeepEqual(before.Lookup(path...), after.Lookup(path...)) {
					t.Errorf("changed legacy field %v", path)
				}
			}
		})
	}
}

func TestF03EmailContactMergePreservesPhoneOrder(t *testing.T) {
	for position := 0; position <= 2; position++ {
		user := models.User{Account: models.Account{AccountID: primitive.NewObjectID().Hex()}}
		user.AddNewEmail("$first@test.com", true)
		user.AddNewEmail("second@test.com", true)
		phone := models.ContactInfo{ID: primitive.NewObjectID(), Type: models.ContactTypePhone, Phone: "+391234567890"}
		user.ContactInfos = append(user.ContactInfos[:position], append([]models.ContactInfo{phone}, user.ContactInfos[position:]...)...)
		id, err := testDBService.AddUser(testInstanceID, user)
		if err != nil {
			t.Fatal(err)
		}
		user.ID, _ = primitive.ObjectIDFromHex(id)
		phone.ID = primitive.NewObjectID()
		phone.Phone = "+391234567891"
		if err := testDBService.ReplacePhoneContactInfo(testInstanceID, id, phone); err != nil {
			t.Fatal(err)
		}
		user.AddNewEmail("$new@test.com", false)
		got, err := testDBService.UpdateUser(testInstanceID, user, "contactInfos")
		if err != nil {
			t.Fatal(err)
		}
		want := append([]models.ContactInfo(nil), user.ContactInfos...)
		want[position] = phone
		if !reflect.DeepEqual(got.ContactInfos, want) {
			t.Errorf("phone position %d or literal emails changed: got %+v want %+v", position, got.ContactInfos, want)
		}
	}
}

func TestF03PreferencesCannotRestoreChannelAfterPhoneChange(t *testing.T) {
	for _, operation := range []string{"delete", "replace"} {
		t.Run(operation, func(t *testing.T) {
			user := models.User{Account: models.Account{AccountID: primitive.NewObjectID().Hex()}}
			user.AddNewPhone("+391234567890", true)
			id, err := testDBService.AddUser(testInstanceID, user)
			if err != nil {
				t.Fatal(err)
			}
			prefs := models.ContactPreferences{PreferredChannels: []string{models.ChannelEmail, models.ChannelWhatsApp}, SubscribedToNewsletter: true}
			if _, err := testDBService.UpdateContactPreferences(testInstanceID, id, prefs); err != nil {
				t.Fatal(err)
			}
			if operation == "delete" {
				_, err = testDBService.DeletePhoneNumber(testInstanceID, id)
			} else {
				err = testDBService.ReplacePhoneContactInfo(testInstanceID, id, models.ContactInfo{ID: primitive.NewObjectID(), Type: models.ContactTypePhone, Phone: "+391234567891"})
			}
			if err != nil {
				t.Fatal(err)
			}
			prefs.SubscribedToNewsletter = false
			if _, err := testDBService.UpdateContactPreferences(testInstanceID, id, prefs); !errors.Is(err, mongo.ErrNoDocuments) {
				t.Fatalf("stale channels accepted: %v", err)
			}
			got, err := testDBService.GetUserByID(testInstanceID, id)
			if err != nil {
				t.Fatal(err)
			}
			if !got.ContactPreferences.SubscribedToNewsletter {
				t.Error("failed preference update partially committed")
			}
			if containsChannel(got.ContactPreferences.PreferredChannels, models.ChannelWhatsApp) {
				t.Error("whatsapp restored without verified phone")
			}
			prefs.PreferredChannels = []string{models.ChannelEmail}
			if _, err := testDBService.UpdateContactPreferences(testInstanceID, id, prefs); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestF03RemoveLegacyContactWithoutID(t *testing.T) {
	user := models.User{
		Account: models.Account{AccountID: primitive.NewObjectID().Hex()},
		ContactInfos: []models.ContactInfo{
			{Type: models.ContactTypeEmail, Email: "first@test.com"},
			{Type: models.ContactTypePhone, Phone: "+391234567890"},
			{Type: models.ContactTypeEmail, Email: "last@test.com"},
		},
	}
	id, err := testDBService.AddUser(testInstanceID, user)
	if err != nil {
		t.Fatal(err)
	}
	user.ID, _ = primitive.ObjectIDFromHex(id)
	got, err := testDBService.RemoveContactInfo(testInstanceID, user.ID, primitive.NilObjectID.Hex())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.ContactInfos, user.ContactInfos[1:]) {
		t.Errorf("must remove only the first matching legacy contact: %+v", got.ContactInfos)
	}
}

func TestF03EmptyPreferenceChannelsRemainOmitted(t *testing.T) {
	for _, channels := range [][]string{nil, {}} {
		user := models.User{Account: models.Account{AccountID: primitive.NewObjectID().Hex()}}
		user.AddNewPhone("+391234567890", false)
		id, err := testDBService.AddUser(testInstanceID, user)
		if err != nil {
			t.Fatal(err)
		}
		got, err := testDBService.UpdateContactPreferences(testInstanceID, id, models.ContactPreferences{PreferredChannels: channels})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := testDBService.getContext()
		raw, err := testDBService.collectionRefUsers(testInstanceID).FindOne(ctx, bson.M{"_id": got.ID}).DecodeBytes()
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if raw.Lookup("contactPreferences", "preferredChannels").Type != 0 {
			t.Error("empty channel list must be omitted, not stored as null/empty")
		}
		if _, err := testDBService.FinalizePhoneVerification(testInstanceID, id); err != nil {
			t.Errorf("verification after empty preferences failed: %v", err)
		}
	}
}

func TestF03ContactConfirmationPreservesConcurrentLedger(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, contactType := range []string{models.ContactTypeEmail, models.ContactTypePhone} {
			user := models.User{Account: models.Account{Type: models.ACCOUNT_TYPE_EMAIL, AccountID: primitive.NewObjectID().Hex() + "@test.com"}}
			user.AddNewEmail(user.Account.AccountID, false)
			user.AddNewPhone("+391234567890", false)
			if legacy {
				for i := range user.ContactInfos {
					user.ContactInfos[i].ID = primitive.NilObjectID
				}
			}
			id, err := testDBService.AddUser(testInstanceID, user)
			if err != nil {
				t.Fatal(err)
			}
			user.ID, _ = primitive.ObjectIDFromHex(id)
			address := user.Account.AccountID
			if contactType == models.ContactTypePhone {
				address = "+391234567890"
			}
			if err := user.ConfirmContactInfo(contactType, address); err != nil {
				t.Fatal(err)
			}
			ci, _ := user.FindContactInfoByTypeAndAddr(contactType, address)
			if ok, err := testDBService.ReservePhoneVerificationSlot(testInstanceID, id, 3, 300); err != nil || !ok {
				t.Fatalf("reserve: %v %v", ok, err)
			}
			got, err := testDBService.ConfirmContactInfo(testInstanceID, user, ci)
			if err != nil {
				t.Fatal(err)
			}
			confirmed, _ := got.FindContactInfoByTypeAndAddr(contactType, address)
			if confirmed.ConfirmedAt <= 0 || len(got.Account.PhoneVerificationAttempts) != 1 {
				t.Error("confirmation or concurrent reservation was lost")
			}
			if contactType == models.ContactTypeEmail && got.Account.AccountConfirmedAt <= 0 {
				t.Error("main account was not confirmed")
			}
			if contactType == models.ContactTypePhone {
				if _, err := testDBService.DeletePhoneNumber(testInstanceID, id); err != nil {
					t.Fatal(err)
				}
				if _, err := testDBService.ConfirmContactInfo(testInstanceID, user, ci); !errors.Is(err, mongo.ErrNoDocuments) {
					t.Errorf("deleted phone confirmation was accepted: %v", err)
				}
			}
		}
	}
}
