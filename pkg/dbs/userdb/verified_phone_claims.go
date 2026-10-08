package userdb

import (
	"errors"
	"strings"
	"time"

	"github.com/influenzanet/user-management-service/pkg/models"
	"github.com/influenzanet/user-management-service/pkg/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// VerifiedPhoneCollection holds one document per verified phone number, keyed by the number in
// E.164 form, naming the account that holds it. It exists because the users collection cannot
// say "this number is verified on at most one account": a phone lives inside the contactInfos
// array of each user document, a unique index over an array of mixed contact types would also
// index the e-mail entries (which have no phone) and collide on them, and the uniqueness
// question spans documents, which no single-document update filter can express. The _id of this
// collection is the serialisation point instead: inserting the same _id twice fails in the
// database, whichever replica the requests land on.
//
// Nothing needs to be cleaned up when a participant deletes or changes the number: a claim whose
// owner no longer holds the number verified is stale, and the next verification of that number
// takes it over (see claimVerifiedPhone).
const VerifiedPhoneCollection = "verifiedPhones"

// ErrPhoneVerifiedElsewhere reports that another account already holds the number verified.
var ErrPhoneVerifiedElsewhere = errors.New("phone number already taken")

// verifiedPhoneClaimRetries bounds the insert / take over loop. Each lap is lost only to another
// request that changed the claim in between, so a few laps are enough.
const verifiedPhoneClaimRetries = 5

func (dbService *UserDBService) collectionRefVerifiedPhones(instanceID string) *mongo.Collection {
	return dbService.DBClient.Database(dbService.DBNamePrefix + instanceID + "_users").Collection(VerifiedPhoneCollection)
}

// verifiedPhoneKey is the _id of the claim on a number: its E.164 form, or the value as stored
// when it cannot be normalised (data written before numbers were normalised).
func verifiedPhoneKey(phone string) string {
	if normalized, err := utils.NormalizePhone(phone); err == nil {
		return normalized
	}
	return strings.TrimSpace(phone)
}

// holdsVerifiedPhone reports whether the account carries the number, in any of its spellings,
// confirmed.
func (dbService *UserDBService) holdsVerifiedPhone(instanceID string, userID primitive.ObjectID, phone string) (bool, error) {
	ctx, cancel := dbService.getContext()
	defer cancel()
	holds, err := dbService.collectionRefUsers(instanceID).CountDocuments(ctx, bson.M{
		"_id": userID,
		"contactInfos": bson.M{"$elemMatch": bson.M{
			"type":        models.ContactTypePhone,
			"phone":       bson.M{"$in": verifiedPhoneForms(phone)},
			"confirmedAt": bson.M{"$gt": 0},
		}},
	})
	return holds > 0, err
}

// afterPhoneClaimHook, when set, runs between the claim and the confirmation of
// FinalizePhoneVerification. It exists so that tests can place another request exactly in that
// window; it is nil in production.
var afterPhoneClaimHook func(userID primitive.ObjectID)

// afterStaleOwnerCheckHook, when set, runs in claimVerifiedPhone between the check that the owner
// on record no longer holds the number and the takeover of its claim; tests use it to let that
// owner confirm in this window. It is nil in production.
var afterStaleOwnerCheckHook func(claimant primitive.ObjectID)

// claimVerifiedPhone makes userID the holder of the verified number, or reports
// ErrPhoneVerifiedElsewhere when another account holds it. It is called right before the
// contact is marked as confirmed, so two accounts that prove the same number at the same moment
// cannot both end up with it: the insert of the claim lets exactly one of them through.
//
// The first result says whether this call put the claim in place (inserted it or took it over),
// as opposed to finding it already in the name of userID. Only a claim put in place by the call
// may be released by that call.
//
// Being the owner on record does not prove the account holds the number: a claim left stale by
// userID itself may be taken over by another account at any moment. The caller therefore confirms
// the claim once the contact is confirmed (see confirmPhoneClaim).
//
// A claim left by an account that no longer holds the number verified (it deleted or replaced
// the number, or was removed) is taken over with a conditional update on the previous owner, so
// of several accounts racing for a stale claim only one wins.
func (dbService *UserDBService) claimVerifiedPhone(instanceID string, userID primitive.ObjectID, phone string) (bool, error) {
	key := verifiedPhoneKey(phone)
	claims := dbService.collectionRefVerifiedPhones(instanceID)

	for attempt := 0; attempt < verifiedPhoneClaimRetries; attempt++ {
		ctx, cancel := dbService.getContext()
		_, err := claims.InsertOne(ctx, bson.M{"_id": key, "userId": userID, "claimedAt": time.Now().Unix(), "rev": 0})
		cancel()
		if err == nil {
			return true, nil
		}
		if !mongo.IsDuplicateKeyError(err) {
			return false, err
		}

		ctx, cancel = dbService.getContext()
		var current struct {
			UserID primitive.ObjectID `bson:"userId"`
			Rev    int64              `bson:"rev"`
		}
		err = claims.FindOne(ctx, bson.M{"_id": key}).Decode(&current)
		cancel()
		if errors.Is(err, mongo.ErrNoDocuments) {
			// The claim was released between the insert and the read: try the insert again.
			continue
		}
		if err != nil {
			return false, err
		}
		if current.UserID == userID {
			return false, nil
		}

		// Somebody else is on record: they hold the number only if it is still verified there.
		holds, err := dbService.holdsVerifiedPhone(instanceID, current.UserID, phone)
		if err != nil {
			return false, err
		}
		if holds {
			return false, ErrPhoneVerifiedElsewhere
		}

		if afterStaleOwnerCheckHook != nil {
			afterStaleOwnerCheckHook(userID)
		}

		// The takeover names the revision that was read: if the owner confirmed in the meantime
		// it has confirmed the claim (see confirmPhoneClaim), the revision moved and the
		// takeover matches nothing.
		ctx, cancel = dbService.getContext()
		res, err := claims.UpdateOne(ctx,
			bson.M{"_id": key, "userId": current.UserID, "rev": revisionFilter(current.Rev)},
			bson.M{"$set": bson.M{"userId": userID, "claimedAt": time.Now().Unix()}, "$inc": bson.M{"rev": 1}},
		)
		cancel()
		if err != nil {
			return false, err
		}
		if res.MatchedCount > 0 {
			return true, nil
		}
		// Another request took the stale claim first: look at it again.
	}
	return false, ErrPhoneVerifiedElsewhere
}

// revisionFilter matches the revision read from a claim; claims written before revisions
// existed have none and read as zero.
func revisionFilter(rev int64) interface{} {
	if rev == 0 {
		return bson.M{"$in": bson.A{int64(0), nil}}
	}
	return rev
}

// confirmPhoneClaim is the second half of the claim: it runs after the contact has been marked
// as confirmed and moves the revision of the claim, with a filter that names userID as the owner.
// This is what closes the window between claiming and confirming. Another account that finds
// userID's claim stale takes it over only against the revision it read, and the account that has
// confirmed moves that revision. So, of the two writes on the claim, whichever lands first
// decides: if the takeover lands first, this update matches nothing and ErrPhoneVerifiedElsewhere
// is returned (the caller then reverts its confirmation); if this one lands first, the takeover
// matches nothing, and the other account looks again and finds the number verified here. Both
// accounts can therefore never keep a confirmation.
func (dbService *UserDBService) confirmPhoneClaim(instanceID string, userID primitive.ObjectID, phone string) error {
	ctx, cancel := dbService.getContext()
	defer cancel()
	res, err := dbService.collectionRefVerifiedPhones(instanceID).UpdateOne(ctx,
		bson.M{"_id": verifiedPhoneKey(phone), "userId": userID},
		bson.M{"$inc": bson.M{"rev": 1}},
	)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrPhoneVerifiedElsewhere
	}
	return nil
}

// revertPhoneConfirmation undoes the confirmation written by one FinalizePhoneVerification call
// that lost the claim. The filter names the account, the number and the very confirmedAt value
// that call wrote, so a confirmation made by anybody else in the meantime is left alone. What it
// undoes is the part that proves the number: confirmedAt goes back to zero and, when the call
// is the one that enabled the whatsapp channel (it was not enabled before), the channel is
// removed again. The e-mail channel that the confirmation also adds is left in place: it is the
// default every account falls back to, and removing it could leave the account with no channel.
// The spent verification code is not restored: the participant asks for a new one. A failure
// here is returned to the caller, who reports it instead of the lost claim.
func (dbService *UserDBService) revertPhoneConfirmation(instanceID string, userID primitive.ObjectID, phone string, confirmedAt int64, whatsAppWasEnabled bool) error {
	ctx, cancel := dbService.getContext()
	defer cancel()

	update := bson.M{"$set": bson.M{
		"contactInfos.$[ci].confirmedAt": 0,
		"timestamps.updatedAt":           time.Now().Unix(),
	}}
	if !whatsAppWasEnabled {
		update["$pull"] = bson.M{"contactPreferences.preferredChannels": models.ChannelWhatsApp}
	}
	_, err := dbService.collectionRefUsers(instanceID).UpdateOne(ctx,
		bson.M{"_id": userID, "contactInfos": bson.M{"$elemMatch": bson.M{
			"type": models.ContactTypePhone, "phone": phone, "confirmedAt": confirmedAt,
		}}},
		update,
		options.Update().SetArrayFilters(options.ArrayFilters{Filters: []interface{}{
			bson.M{"ci.type": models.ContactTypePhone, "ci.phone": phone, "ci.confirmedAt": confirmedAt},
		}}),
	)
	return err
}

// releaseVerifiedPhoneClaim drops the claim userID took when the confirmation that followed
// it did not happen. The filter names the owner, so a claim that moved on is left alone. A
// failure here is harmless: the claim is stale and the next verification takes it over.
func (dbService *UserDBService) releaseVerifiedPhoneClaim(instanceID string, userID primitive.ObjectID, phone string) {
	ctx, cancel := dbService.getContext()
	defer cancel()
	_, _ = dbService.collectionRefVerifiedPhones(instanceID).DeleteOne(ctx, bson.M{"_id": verifiedPhoneKey(phone), "userId": userID})
}

// verifiedPhoneForms lists the spellings under which an account may hold the same number: the
// one given and its E.164 form.
func verifiedPhoneForms(phone string) []string {
	forms := []string{phone}
	if normalized, err := utils.NormalizePhone(phone); err == nil && normalized != phone {
		forms = append(forms, normalized)
	}
	return forms
}
