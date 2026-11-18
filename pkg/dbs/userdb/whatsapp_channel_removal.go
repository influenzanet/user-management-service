package userdb

import (
	"errors"
	"time"

	"github.com/influenzanet/user-management-service/pkg/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// FindVerifiedPhoneHolder returns the account that holds the number verified, found through the
// claim on the number (see VerifiedPhoneCollection). found is false when there is no claim, or
// when the account on record no longer holds the number verified (a stale claim): a number that
// is only pending verification, or that an account has deleted or replaced, belongs to nobody.
func (dbService *UserDBService) FindVerifiedPhoneHolder(instanceID string, phone string) (userID primitive.ObjectID, found bool, err error) {
	ctx, cancel := dbService.getContext()
	var claim storedClaim
	err = dbService.collectionRefVerifiedPhones(instanceID).FindOne(ctx, bson.M{"_id": verifiedPhoneKey(phone)}).Decode(&claim)
	cancel()
	if errors.Is(err, mongo.ErrNoDocuments) {
		return primitive.NilObjectID, false, nil
	}
	if err != nil {
		return primitive.NilObjectID, false, err
	}
	holds, err := dbService.holdsVerifiedPhone(instanceID, claim.UserID, phone)
	if err != nil || !holds {
		return primitive.NilObjectID, false, err
	}
	return claim.UserID, true, nil
}

// RemoveWhatsAppChannel takes whatsapp out of the account's notification channels and leaves
// everything else as it is: the other channels, the subscriptions and the phone number itself.
// Nothing is added in its place, so an account reached on whatsapp only is left with no channel.
//
// With phone set, the account must still hold that number verified: an opt-out sent from a
// number does not reach the channel the account has since enabled for another number.
//
// The filter requires whatsapp among the stored channels, so of several calls for the same
// account (Meta repeats its notifications) exactly one removes it. removed tells whether this
// call did; before is the account as it was, and is set only when removed. A legacy account
// (no channel list stored) reads as e-mail and has no whatsapp to remove.
func (dbService *UserDBService) RemoveWhatsAppChannel(instanceID string, userID primitive.ObjectID, phone string) (before models.User, removed bool, err error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	filter := bson.M{"_id": userID, "contactPreferences.preferredChannels": models.ChannelWhatsApp}
	if phone != "" {
		filter["contactInfos"] = bson.M{"$elemMatch": bson.M{
			"type":        models.ContactTypePhone,
			"phone":       bson.M{"$in": verifiedPhoneForms(phone)},
			"confirmedAt": bson.M{"$gt": 0},
		}}
	}
	update := bson.M{
		"$pull": bson.M{"contactPreferences.preferredChannels": models.ChannelWhatsApp},
		"$set":  bson.M{"timestamps.updatedAt": time.Now().Unix()},
	}
	err = dbService.collectionRefUsers(instanceID).FindOneAndUpdate(ctx, filter, update,
		options.FindOneAndUpdate().SetReturnDocument(options.Before)).Decode(&before)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return models.User{}, false, nil
	}
	if err != nil {
		return models.User{}, false, err
	}
	return before, true, nil
}
