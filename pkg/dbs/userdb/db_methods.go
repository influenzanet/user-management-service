package userdb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/coneno/logger"
	"github.com/influenzanet/go-utils/pkg/constants"
	"github.com/influenzanet/user-management-service/pkg/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func (dbService *UserDBService) AddUser(instanceID string, user models.User) (id string, err error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	filter := bson.M{"account.accountID": user.Account.AccountID}
	upsert := true
	opts := options.UpdateOptions{
		Upsert: &upsert,
	}
	res, err := dbService.collectionRefUsers(instanceID).UpdateOne(ctx, filter, bson.M{
		"$setOnInsert": user,
	}, &opts)
	if err != nil {
		return
	}

	if res.UpsertedCount < 1 {
		err = errors.New("user already exists")
		return
	}

	id = res.UpsertedID.(primitive.ObjectID).Hex()
	return
}

func (dbService *UserDBService) updateUser(instanceID string, filter bson.M, update interface{}) (models.User, error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	elem := models.User{}
	err := dbService.collectionRefUsers(instanceID).FindOneAndUpdate(ctx, filter, update,
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&elem)
	return elem, err
}

// UpdateUser saves only explicitly owned fields, never a complete stale user snapshot.
// Phone state and channel preferences belong to their dedicated atomic writers.
// The contactInfos mask is email-only: current phone entries are retained in place.
func (dbService *UserDBService) UpdateUser(instanceID string, user models.User, field string, otherFields ...string) (models.User, error) {
	allowed := bson.M{
		"account.accountID":                                user.Account.AccountID,
		"account.accountConfirmedAt":                       user.Account.AccountConfirmedAt,
		"account.verificationCode":                         user.Account.VerificationCode,
		"account.failedLoginAttempts":                      user.Account.FailedLoginAttempts,
		"account.passwordResetTriggers":                    user.Account.PasswordResetTriggers,
		"timestamps.lastLogin":                             user.Timestamps.LastLogin,
		"timestamps.lastTokenRefresh":                      user.Timestamps.LastTokenRefresh,
		"timestamps.markedForDeletion":                     user.Timestamps.MarkedForDeletion,
		"profiles":                                         user.Profiles,
		"roles":                                            user.Roles,
		"contactPreferences.sendNewsletterTo":              user.ContactPreferences.SendNewsletterTo,
		"contactPreferences.subscribedToNewsletter":        user.ContactPreferences.SubscribedToNewsletter,
		"contactPreferences.subscribedToWeekly":            user.ContactPreferences.SubscribedToWeekly,
		"contactPreferences.receiveWeeklyMessageDayOfWeek": user.ContactPreferences.ReceiveWeeklyMessageDayOfWeek,
	}
	set := bson.M{"timestamps.updatedAt": time.Now().Unix()}
	for _, name := range append([]string{field}, otherFields...) {
		if name == "contactInfos" {
			set[name] = emailContactsWithCurrentPhones(user.ContactInfos)
			continue
		}
		value, ok := allowed[name]
		if !ok {
			return models.User{}, fmt.Errorf("unsupported user update field: %s", name)
		}
		// Pipeline strings (including profile aliases) must be data, not expressions.
		set[name] = bson.M{"$literal": value}
	}
	return dbService.updateUser(instanceID, bson.M{"_id": user.ID}, mongo.Pipeline{bson.D{{Key: "$set", Value: set}}})
}

// Preserve phone slots in the incoming contact order, filling them from the current
// document. Deleted phones leave no slot; newly added phones are appended. This is
// used only by account-email changes, which may edit several email entries at once.
func emailContactsWithCurrentPhones(incoming []models.ContactInfo) bson.M {
	parts := bson.A{}
	phoneSlots := 0
	for _, ci := range incoming {
		if ci.Type == models.ContactTypePhone {
			parts = append(parts, bson.M{"$slice": bson.A{"$$phones", phoneSlots, 1}})
			phoneSlots++
		} else {
			parts = append(parts, bson.M{"$literal": []models.ContactInfo{ci}})
		}
	}
	parts = append(parts, bson.M{"$slice": bson.A{"$$phones", phoneSlots,
		bson.M{"$add": bson.A{bson.M{"$size": "$$phones"}, 1}}}})
	return bson.M{"$let": bson.M{
		"vars": bson.M{"phones": bson.M{"$filter": bson.M{
			"input": bson.M{"$ifNull": bson.A{"$contactInfos", bson.A{}}},
			"as":    "ci", "cond": bson.M{"$eq": bson.A{"$$ci.type", models.ContactTypePhone}},
		}}},
		"in": bson.M{"$concatArrays": parts},
	}}
}

func (dbService *UserDBService) AddEmailContactInfo(instanceID string, userID primitive.ObjectID, ci models.ContactInfo) (models.User, error) {
	if ci.Type != models.ContactTypeEmail {
		return models.User{}, errors.New("wrong contact type")
	}
	return dbService.updateUser(instanceID, bson.M{"_id": userID}, mongo.Pipeline{bson.D{{Key: "$set", Value: bson.M{
		"contactInfos": bson.M{"$concatArrays": bson.A{
			bson.M{"$ifNull": bson.A{"$contactInfos", bson.A{}}}, bson.M{"$literal": []models.ContactInfo{ci}},
		}},
		"timestamps.updatedAt": time.Now().Unix(),
	}}}})
}

func (dbService *UserDBService) RemoveContactInfo(instanceID string, userID primitive.ObjectID, contactID string) (models.User, error) {
	id, err := primitive.ObjectIDFromHex(contactID)
	if err != nil {
		return models.User{}, err
	}
	// Match the model's first-ID semantics, including legacy entries whose zero ID
	// is omitted in BSON. Slicing the current array never restores a stale phone.
	index := bson.M{"$indexOfArray": bson.A{bson.M{"$map": bson.M{
		"input": bson.M{"$ifNull": bson.A{"$contactInfos", bson.A{}}},
		"as":    "ci", "in": bson.M{"$ifNull": bson.A{"$$ci._id", primitive.NilObjectID}},
	}}, id}}
	filter := bson.M{"_id": userID, "$expr": bson.M{"$gte": bson.A{index, 0}}}
	return dbService.updateUser(instanceID, filter, mongo.Pipeline{bson.D{{Key: "$set", Value: bson.M{
		"contactInfos": bson.M{"$let": bson.M{
			"vars": bson.M{"index": index},
			"in": bson.M{"$concatArrays": bson.A{
				bson.M{"$slice": bson.A{"$contactInfos", "$$index"}},
				bson.M{"$slice": bson.A{"$contactInfos", bson.M{"$add": bson.A{"$$index", 1}},
					bson.M{"$add": bson.A{bson.M{"$size": "$contactInfos"}, 1}}}},
			}},
		}},
		"timestamps.updatedAt": time.Now().Unix(),
	}}}})
}

// Confirm only the contact that was validated. A replaced/deleted phone must not
// be restored, and confirming an email must not rewrite any phone state.
func (dbService *UserDBService) ConfirmContactInfo(instanceID string, user models.User, ci models.ContactInfo) (models.User, error) {
	contact := bson.M{"type": ci.Type}
	// Legacy email entries may predate contact IDs (the zero ID is omitted in BSON).
	if !ci.ID.IsZero() {
		contact["_id"] = ci.ID
	}
	switch ci.Type {
	case models.ContactTypeEmail:
		contact["email"] = ci.Email
	case models.ContactTypePhone:
		contact["phone"] = ci.Phone
	default:
		return models.User{}, errors.New("wrong contact type")
	}
	filter := bson.M{"_id": user.ID, "contactInfos": bson.M{"$elemMatch": contact}}
	set := bson.M{"contactInfos.$.confirmedAt": ci.ConfirmedAt, "timestamps.updatedAt": time.Now().Unix()}
	if ci.Type == models.ContactTypeEmail && user.Account.Type == models.ACCOUNT_TYPE_EMAIL && user.Account.AccountID == ci.Email {
		filter["account.accountID"] = ci.Email
		set["account.accountConfirmedAt"] = ci.ConfirmedAt
	}
	return dbService.updateUser(instanceID, filter, bson.M{"$set": set})
}

func (dbService *UserDBService) GetUserByID(instanceID string, id string) (models.User, error) {
	_id, _ := primitive.ObjectIDFromHex(id)
	filter := bson.M{"_id": _id}

	ctx, cancel := dbService.getContext()
	defer cancel()

	elem := models.User{}
	err := dbService.collectionRefUsers(instanceID).FindOne(ctx, filter).Decode(&elem)

	return elem, err
}

func (dbService *UserDBService) GetUserByAccountID(instanceID string, username string) (models.User, error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	elem := models.User{}
	filter := bson.M{"account.accountID": username}
	err := dbService.collectionRefUsers(instanceID).FindOne(ctx, filter).Decode(&elem)

	return elem, err
}

func (dbService *UserDBService) UpdateUserPassword(instanceID string, userID string, newPassword string) error {
	ctx, cancel := dbService.getContext()
	defer cancel()

	_id, _ := primitive.ObjectIDFromHex(userID)
	filter := bson.M{"_id": _id}
	update := bson.M{"$set": bson.M{"account.password": newPassword, "timestamps.lastPasswordChange": time.Now().Unix()}}
	_, err := dbService.collectionRefUsers(instanceID).UpdateOne(ctx, filter, update)
	if err != nil {
		return err
	}
	return nil
}

func (dbService *UserDBService) SaveFailedLoginAttempt(instanceID string, userID string) error {
	ctx, cancel := dbService.getContext()
	defer cancel()

	_id, _ := primitive.ObjectIDFromHex(userID)
	filter := bson.M{"_id": _id}
	update := bson.M{"$push": bson.M{"account.failedLoginAttempts": time.Now().Unix()}}
	_, err := dbService.collectionRefUsers(instanceID).UpdateOne(ctx, filter, update)
	if err != nil {
		return err
	}
	return nil
}

func (dbService *UserDBService) SavePasswordResetTrigger(instanceID string, userID string) error {
	ctx, cancel := dbService.getContext()
	defer cancel()

	_id, _ := primitive.ObjectIDFromHex(userID)
	filter := bson.M{"_id": _id}
	update := bson.M{"$push": bson.M{"account.passwordResetTriggers": time.Now().Unix()}}
	_, err := dbService.collectionRefUsers(instanceID).UpdateOne(ctx, filter, update)
	if err != nil {
		return err
	}
	return nil
}

// ReservePhoneVerificationSlot atomically consumes one send slot of the phone verification
// rate limit: the filter admits the document only while fewer than maxAttempts recorded
// attempts fall inside the window, and the pipeline update appends the new attempt to the
// in-window subset in the same operation (also normalising a legacy null field to an
// array), so expired attempts are pruned on every reservation and the array is bounded by
// the window's worth of entries. Check, increment and pruning are therefore a single
// find-and-modify: concurrent requests, including ones running on other replicas, cannot
// both pass the check on the same free slot.
// Returns false when no document matched, i.e. the budget is used up or the user does not
// exist — callers that already hold the user can safely map false to "budget exhausted".
func (dbService *UserDBService) ReservePhoneVerificationSlot(instanceID string, userID string, maxAttempts int, windowSeconds int64) (bool, error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	_id, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		return false, err
	}
	now := time.Now().Unix()
	windowStart := now - windowSeconds

	attemptsInWindow := bson.M{"$filter": bson.M{
		"input": bson.M{"$ifNull": bson.A{"$account.phoneVerificationAttempts", bson.A{}}},
		"as":    "attempt",
		"cond":  bson.M{"$gt": bson.A{"$$attempt", windowStart}},
	}}
	filter := bson.M{
		"_id":   _id,
		"$expr": bson.M{"$lt": bson.A{bson.M{"$size": attemptsInWindow}, maxAttempts}},
	}
	update := mongo.Pipeline{bson.D{{Key: "$set", Value: bson.M{
		"account.phoneVerificationAttempts": bson.M{"$concatArrays": bson.A{
			attemptsInWindow,
			bson.A{now},
		}},
	}}}}
	res, err := dbService.collectionRefUsers(instanceID).UpdateOne(ctx, filter, update)
	if err != nil {
		return false, err
	}
	return res.MatchedCount > 0, nil
}

// SetPhoneVerificationCode persists the phone verification code with a targeted $set, so
// concurrently updated fields (like the atomically managed attempts array) are not rewritten.
func (dbService *UserDBService) SetPhoneVerificationCode(instanceID string, userID string, code models.VerificationCode) error {
	ctx, cancel := dbService.getContext()
	defer cancel()

	_id, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		return err
	}
	filter := bson.M{"_id": _id}
	update := bson.M{"$set": bson.M{
		"account.phoneVerificationCode": code,
		"timestamps.updatedAt":          time.Now().Unix(),
	}}
	_, err = dbService.collectionRefUsers(instanceID).UpdateOne(ctx, filter, update)
	return err
}

// AddPhoneContactInfoIfAbsent appends the phone contact info only when the user has no phone
// contact info yet; the guard lives in the filter, so two concurrent requests cannot both add
// one. Returns false when a phone contact info already exists.
func (dbService *UserDBService) AddPhoneContactInfoIfAbsent(instanceID string, userID string, ci models.ContactInfo) (bool, error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	_id, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		return false, err
	}
	filter := bson.M{
		"_id":          _id,
		"contactInfos": bson.M{"$not": bson.M{"$elemMatch": bson.M{"type": models.ContactTypePhone}}},
	}
	update := bson.M{
		"$push": bson.M{"contactInfos": ci},
		"$set":  bson.M{"timestamps.updatedAt": time.Now().Unix()},
	}
	res, err := dbService.collectionRefUsers(instanceID).UpdateOne(ctx, filter, update)
	if err != nil {
		return false, err
	}
	return res.MatchedCount > 0, nil
}

// ReplacePhoneContactInfo overwrites the user's phone contact info entry in place with a
// targeted positional $set, leaving every other field of the document untouched.
func (dbService *UserDBService) ReplacePhoneContactInfo(instanceID string, userID string, ci models.ContactInfo) error {
	ctx, cancel := dbService.getContext()
	defer cancel()

	_id, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		return err
	}
	filter := bson.M{"_id": _id}
	update := bson.M{
		"$set": bson.M{
			"contactInfos.$[ci]":   ci,
			"timestamps.updatedAt": time.Now().Unix(),
		},
		// The replacement is unverified, so the whatsapp channel loses the destination it
		// stood for and goes with it, in this same operation: leaving it on would describe a
		// delivery the platform cannot make until the new number is verified. Deleting a
		// number already revokes it the same way.
		"$pull": bson.M{
			"contactPreferences.preferredChannels": models.ChannelWhatsApp,
		},
	}
	opts := options.Update().SetArrayFilters(options.ArrayFilters{
		Filters: []interface{}{bson.M{"ci.type": models.ContactTypePhone}},
	})
	_, err = dbService.collectionRefUsers(instanceID).UpdateOne(ctx, filter, update, opts)
	return err
}

// contactInfoElementFilter builds the array filter that selects one contact info by type and
// address, so updates reach a single entry of contactInfos and leave the rest of the array,
// and of the document, as they are.
func contactInfoElementFilter(contactType string, address string) bson.M {
	elem := bson.M{"ci.type": contactType}
	switch contactType {
	case models.ContactTypeEmail:
		elem["ci.email"] = address
	case models.ContactTypePhone:
		elem["ci.phone"] = address
	}
	return elem
}

// SetContactVerificationSentAt stamps confirmationLinkSentAt on the matching contact info with
// a targeted $set (the cooldown marker of both the email and the phone flow).
func (dbService *UserDBService) SetContactVerificationSentAt(instanceID string, userID string, contactType string, address string, sentAt int64) error {
	ctx, cancel := dbService.getContext()
	defer cancel()

	_id, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		return err
	}
	filter := bson.M{"_id": _id}
	update := bson.M{"$set": bson.M{"contactInfos.$[ci].confirmationLinkSentAt": sentAt}}
	opts := options.Update().SetArrayFilters(options.ArrayFilters{
		Filters: []interface{}{contactInfoElementFilter(contactType, address)},
	})
	_, err = dbService.collectionRefUsers(instanceID).UpdateOne(ctx, filter, update, opts)
	return err
}

// FinalizePhoneVerification records a successful phone verification with targeted updates: the
// phone contact info is marked as confirmed, the whatsapp channel is enabled together with
// email (so enabling whatsapp never silences email delivery), and the spent code is cleared.
// The attempts ledger is deliberately absent from the update: a verification sends no message,
// so it has no attempt to account for and must not rewrite what the atomic reservation
// maintains — a field that is never named cannot be clobbered.
// The filter requires a phone contact info to still be there, so a number deleted while the code
// was being verified yields mongo.ErrNoDocuments instead of enabling the whatsapp channel for a
// user who has no phone left.
func (dbService *UserDBService) FinalizePhoneVerification(instanceID string, userID string) (models.User, error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	_id, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		return models.User{}, err
	}
	now := time.Now().Unix()
	update := bson.M{
		"$set": bson.M{
			"contactInfos.$[ci].confirmedAt": now,
			"account.phoneVerificationCode":  models.VerificationCode{},
			"timestamps.updatedAt":           now,
		},
		"$addToSet": bson.M{
			"contactPreferences.preferredChannels": bson.M{
				"$each": bson.A{models.ChannelEmail, models.ChannelWhatsApp},
			},
		},
	}
	opts := options.FindOneAndUpdate().
		SetArrayFilters(options.ArrayFilters{
			Filters: []interface{}{bson.M{"ci.type": models.ContactTypePhone}},
		}).
		SetReturnDocument(options.After)

	filter := bson.M{
		"_id":          _id,
		"contactInfos": bson.M{"$elemMatch": bson.M{"type": models.ContactTypePhone}},
	}

	var updated models.User
	err = dbService.collectionRefUsers(instanceID).FindOneAndUpdate(ctx, filter, update, opts).Decode(&updated)
	return updated, err
}

func (dbService *UserDBService) UpdateAccountPreferredLang(instanceID string, userID string, lang string) (models.User, error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	_id, _ := primitive.ObjectIDFromHex(userID)
	filter := bson.M{"_id": _id}

	elem := models.User{}

	rd := options.After
	fro := options.FindOneAndUpdateOptions{
		ReturnDocument: &rd,
	}
	update := bson.M{"$set": bson.M{"account.preferredLanguage": lang, "timestamps.updatedAt": time.Now().Unix()}}
	err := dbService.collectionRefUsers(instanceID).FindOneAndUpdate(ctx, filter, update, &fro).Decode(&elem)
	return elem, err
}

func (dbService *UserDBService) UpdateContactPreferences(instanceID string, userID string, prefs models.ContactPreferences) (models.User, error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	_id, _ := primitive.ObjectIDFromHex(userID)
	filter := bson.M{"_id": _id}
	for _, channel := range prefs.PreferredChannels {
		if channel == models.ChannelWhatsApp {
			// The phone may have been removed or replaced since the API validation.
			filter["contactInfos"] = bson.M{"$elemMatch": bson.M{
				"type": models.ContactTypePhone, "confirmedAt": bson.M{"$gt": 0},
			}}
			break
		}
	}

	elem := models.User{}

	rd := options.After
	fro := options.FindOneAndUpdateOptions{
		ReturnDocument: &rd,
	}
	set := bson.M{
		"contactPreferences.subscribedToNewsletter":        prefs.SubscribedToNewsletter,
		"contactPreferences.sendNewsletterTo":              prefs.SendNewsletterTo,
		"contactPreferences.subscribedToWeekly":            prefs.SubscribedToWeekly,
		"contactPreferences.receiveWeeklyMessageDayOfWeek": prefs.ReceiveWeeklyMessageDayOfWeek,
		"timestamps.updatedAt":                             time.Now().Unix(),
	}
	update := bson.M{"$set": set}
	if len(prefs.PreferredChannels) == 0 {
		// Preserve the model's omitempty semantics; null would break later $addToSet.
		update["$unset"] = bson.M{"contactPreferences.preferredChannels": ""}
	} else {
		set["contactPreferences.preferredChannels"] = prefs.PreferredChannels
	}
	err := dbService.collectionRefUsers(instanceID).FindOneAndUpdate(ctx, filter, update, &fro).Decode(&elem)
	return elem, err
}

func (dbService *UserDBService) UpdateLoginTime(instanceID string, id string) error {
	ctx, cancel := dbService.getContext()
	defer cancel()

	_id, _ := primitive.ObjectIDFromHex(id)
	filter := bson.M{"_id": _id}
	update := bson.M{"$set": bson.M{
		"timestamps.lastLogin":         time.Now().Unix(),
		"timestamps.updatedAt":         time.Now().Unix(),
		"timestamps.markedForDeletion": 0,
	}}
	_, err := dbService.collectionRefUsers(instanceID).UpdateOne(ctx, filter, update)
	if err != nil {
		return err
	}
	return nil
}

func (dbService *UserDBService) UpdateReminderToConfirmSentAtTime(instanceID string, id string) error {
	ctx, cancel := dbService.getContext()
	defer cancel()

	_id, _ := primitive.ObjectIDFromHex(id)
	filter := bson.M{"_id": _id}
	update := bson.M{"$set": bson.M{"timestamps.reminderToConfirmSentAt": time.Now().Unix()}}
	_, err := dbService.collectionRefUsers(instanceID).UpdateOne(ctx, filter, update)
	if err != nil {
		return err
	}
	return nil
}

func (dbService *UserDBService) UpdateMarkedForDeletionTime(instanceID string, id string, dT int64, reset bool) (bool, error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	_id, _ := primitive.ObjectIDFromHex(id)
	if reset {
		filter := bson.M{"_id": _id}
		update := bson.M{"$set": bson.M{"timestamps.markedForDeletion": 0}}
		res, err := dbService.collectionRefUsers(instanceID).UpdateOne(ctx, filter, update)
		if err != nil {
			return false, err
		}
		if res.MatchedCount > 0 {
			return true, nil
		}
		return false, nil
	}
	filter := bson.M{}
	filter["$and"] = bson.A{
		bson.M{"_id": _id},
		bson.M{"timestamps.markedForDeletion": bson.M{"$not": bson.M{"$gt": 0}}},
	}
	update := bson.M{"$set": bson.M{"timestamps.markedForDeletion": time.Now().Unix() + dT}}
	res, err := dbService.collectionRefUsers(instanceID).UpdateOne(ctx, filter, update)
	if err != nil {
		return false, err
	}
	if res.MatchedCount > 0 {
		return true, nil
	}
	return false, nil
}

func (dbService *UserDBService) CountRecentlyCreatedUsers(instanceID string, interval int64) (count int64, err error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	filter := bson.M{"timestamps.createdAt": bson.M{"$gt": time.Now().Unix() - interval}}
	count, err = dbService.collectionRefUsers(instanceID).CountDocuments(ctx, filter)
	return
}

func (dbService *UserDBService) DeleteUser(instanceID string, id string) error {
	_id, _ := primitive.ObjectIDFromHex(id)
	filter := bson.M{"_id": _id}

	ctx, cancel := dbService.getContext()
	defer cancel()
	res, err := dbService.collectionRefUsers(instanceID).DeleteOne(ctx, filter, nil)
	if err != nil {
		return err
	}
	if res.DeletedCount < 1 {
		return errors.New("no user found with the given id")
	}
	return nil
}

func (dbService *UserDBService) DeleteUnverfiedUsers(instanceID string, createdBefore int64) (int64, error) {
	filter := bson.M{}
	filter["$and"] = bson.A{
		bson.M{"account.accountConfirmedAt": 0},
		bson.M{"timestamps.createdAt": bson.M{"$lt": createdBefore}},
	}

	ctx, cancel := dbService.getContext()
	defer cancel()
	res, err := dbService.collectionRefUsers(instanceID).DeleteMany(ctx, filter, nil)
	if err != nil {
		return 0, err
	}

	return res.DeletedCount, nil
}

func (dbService *UserDBService) FindUsersMarkedForDeletion(instanceID string) (users []models.User, err error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	filter := bson.M{}
	filter["$and"] = bson.A{
		bson.M{"timestamps.markedForDeletion": bson.M{"$gt": 0}},
		bson.M{"timestamps.markedForDeletion": bson.M{"$lt": time.Now().Unix()}},
	}

	cur, err := dbService.collectionRefUsers(instanceID).Find(
		ctx,
		filter,
	)

	if err != nil {
		return users, err
	}
	defer cur.Close(ctx)

	users = []models.User{}
	for cur.Next(ctx) {
		var result models.User
		err := cur.Decode(&result)
		if err != nil {
			return users, err
		}

		users = append(users, result)
	}
	if err := cur.Err(); err != nil {
		return users, err
	}

	return users, nil
}

func (dbService *UserDBService) FindNonParticipantUsers(instanceID string) (users []models.User, err error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	filter := bson.M{
		"roles": bson.M{"$elemMatch": bson.M{"$in": bson.A{
			constants.USER_ROLE_SERVICE_ACCOUNT,
			constants.USER_ROLE_RESEARCHER,
			constants.USER_ROLE_ADMIN,
		}}},
	}
	cur, err := dbService.collectionRefUsers(instanceID).Find(
		ctx,
		filter,
	)

	if err != nil {
		return users, err
	}
	defer cur.Close(ctx)

	users = []models.User{}
	for cur.Next(ctx) {
		var result models.User
		err := cur.Decode(&result)
		if err != nil {
			return users, err
		}

		users = append(users, result)
	}
	if err := cur.Err(); err != nil {
		return users, err
	}

	return users, nil
}

func (dbService *UserDBService) FindInactiveUsers(instanceID string, dT int64) (users []models.User, err error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	filter := bson.M{}
	filter["$and"] = bson.A{
		bson.M{
			"roles": bson.M{"$nin": bson.A{
				constants.USER_ROLE_SERVICE_ACCOUNT,
				constants.USER_ROLE_RESEARCHER,
				constants.USER_ROLE_ADMIN,
			}},
		},
		bson.M{"timestamps.lastLogin": bson.M{"$lt": time.Now().Unix() - dT}},
		bson.M{"timestamps.lastTokenRefresh": bson.M{"$lt": time.Now().Unix() - dT}},
		bson.M{"timestamps.markedForDeletion": bson.M{"$not": bson.M{"$gt": 0}}},
	}

	cur, err := dbService.collectionRefUsers(instanceID).Find(
		ctx,
		filter,
	)

	if err != nil {
		return users, err
	}
	defer cur.Close(ctx)

	users = []models.User{}
	for cur.Next(ctx) {
		var result models.User
		err := cur.Decode(&result)
		if err != nil {
			return users, err
		}

		users = append(users, result)
	}
	if err := cur.Err(); err != nil {
		return users, err
	}

	return users, nil
}

type UserFilter struct {
	OnlyConfirmed   bool
	ReminderWeekDay int32
}

func (dbService *UserDBService) PerfomActionForUsers(
	ctx context.Context,
	instanceID string,
	filters UserFilter,
	cbk func(instanceID string, user models.User, args ...interface{}) error,
	args ...interface{},
) (err error) {
	filter := bson.M{}
	if filters.OnlyConfirmed {
		filter["account.accountConfirmedAt"] = bson.M{"$gt": 0}
	}
	if filters.ReminderWeekDay > -1 {
		filter["contactPreferences.receiveWeeklyMessageDayOfWeek"] = filters.ReminderWeekDay
	}

	batchSize := int32(32)
	options := options.FindOptions{
		NoCursorTimeout: &dbService.noCursorTimeout,
		BatchSize:       &batchSize,
	}

	cur, err := dbService.collectionRefUsers(instanceID).Find(
		ctx,
		filter,
		&options,
	)
	if err != nil {
		return err
	}
	defer cur.Close(ctx)

	for cur.Next(ctx) {
		if ctx.Err() != nil {
			logger.Debug.Println(ctx.Err())
			return ctx.Err()
		}
		var result models.User
		err := cur.Decode(&result)
		if err != nil {
			logger.Error.Printf("wrong user model %v, %v", result, err)
			continue
		}

		if err := cbk(instanceID, result, args...); err != nil {
			logger.Debug.Printf("error in callback: %v", err)
			return err
		}
	}
	if err := cur.Err(); err != nil {
		return err
	}
	return nil
}

func (dbService *UserDBService) SendReminderToConfirmAccountLoop(
	ctx context.Context,
	instanceID string,
	createdBefore int64,
	cbk func(instanceID string, user models.User, args ...interface{}) error,
	args ...interface{},
) (err error) {
	filter := bson.M{}
	filter["$and"] = bson.A{
		bson.M{"account.accountConfirmedAt": bson.M{"$lt": 1}},
		bson.M{"timestamps.reminderToConfirmSentAt": bson.M{"$lt": 1}},
		bson.M{"timestamps.createdAt": bson.M{"$lt": createdBefore}},
	}

	batchSize := int32(32)
	options := options.FindOptions{
		NoCursorTimeout: &dbService.noCursorTimeout,
		BatchSize:       &batchSize,
	}

	cur, err := dbService.collectionRefUsers(instanceID).Find(
		ctx,
		filter,
		&options,
	)
	if err != nil {
		return err
	}
	defer cur.Close(ctx)

	for cur.Next(ctx) {
		if ctx.Err() != nil {
			logger.Debug.Println(ctx.Err())
			return ctx.Err()
		}
		var result models.User
		err := cur.Decode(&result)
		if err != nil {
			logger.Error.Printf("wrong user model %v, %v", result, err)
			continue
		}

		if err := cbk(instanceID, result, args...); err != nil {
			logger.Debug.Printf("error in callback: %v", err)
			continue
		}

		if err := dbService.UpdateReminderToConfirmSentAtTime(instanceID, result.ID.Hex()); err != nil {
			logger.Error.Printf("unexpected error: %v", err)
			continue
		}
	}
	if err := cur.Err(); err != nil {
		return err
	}
	return nil
}

func (dbService *UserDBService) CreateIndexForUser(instanceID string) error {
	ctx, cancel := dbService.getContext()
	defer cancel()

	_, err := dbService.collectionRefUsers(instanceID).Indexes().CreateMany(
		ctx, []mongo.IndexModel{
			{
				Keys: bson.D{
					{Key: "timestamps.markedForDeletion", Value: 1},
				},
			},
			{
				Keys: bson.D{
					{Key: "account.accountID", Value: 1},
				},
			},
			{
				Keys: bson.D{
					{Key: "timestamps.createdAt", Value: 1},
				},
			},
			{
				Keys: bson.D{
					{Key: "account.accountConfirmedAt", Value: 1},
					{Key: "timestamps.createdAt", Value: 1},
				},
			},
			{
				Keys: bson.D{
					{Key: "contactPreferences.receiveWeeklyMessageDayOfWeek", Value: 1},
				},
			},
		},
	)
	return err
}

// IsPhoneNumberTaken checks if a phone number is already in use
func (dbService *UserDBService) IsPhoneNumberTaken(ctx context.Context, instanceID string, phoneNumbers []string) (bool, error) {
	return dbService.IsPhoneNumberTakenExcludingUser(ctx, instanceID, phoneNumbers, "")
}

// IsPhoneNumberTakenExcludingUser checks if a phone number is taken by any user except the specified one
func (dbService *UserDBService) IsPhoneNumberTakenExcludingUser(ctx context.Context, instanceID string, phoneNumbers []string, excludeUserID string) (bool, error) {
	filter := bson.M{
		"contactInfos": bson.M{
			"$elemMatch": bson.M{
				"type":  models.ContactTypePhone,
				"phone": bson.M{"$in": phoneNumbers},
			},
		},
	}

	// Exclude specific user if provided
	if excludeUserID != "" {
		userObjID, err := primitive.ObjectIDFromHex(excludeUserID)
		if err == nil {
			filter["_id"] = bson.M{"$ne": userObjID}
		} else {
			logger.Warning.Printf("IsPhoneNumberTakenExcludingUser: invalid excludeUserID: %v", err)
		}
	}

	count, err := dbService.collectionRefUsers(instanceID).CountDocuments(ctx, filter)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// DeletePhoneNumber removes a phone number from a user
func (dbService *UserDBService) DeletePhoneNumber(instanceID, userID string) (models.User, error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	userObjID, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		return models.User{}, err
	}

	filter := bson.M{"_id": userObjID}
	update := bson.M{
		"$pull": bson.M{
			"contactInfos":                         bson.M{"type": models.ContactTypePhone},
			"contactPreferences.preferredChannels": models.ChannelWhatsApp,
		},
		"$set": bson.M{
			"timestamps.updatedAt":          time.Now().Unix(),
			"account.phoneVerificationCode": bson.M{},
		},
		"$unset": bson.M{
			"contactPreferences.whatsappNumber": "",
		},
	}

	var updatedUser models.User
	err = dbService.collectionRefUsers(instanceID).FindOneAndUpdate(
		ctx,
		filter,
		update,
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&updatedUser)

	if err != nil {
		return models.User{}, err
	}

	// Ensure at least email channel after removing whatsapp
	hasEmail := false
	for _, ch := range updatedUser.ContactPreferences.PreferredChannels {
		if ch == models.ChannelEmail {
			hasEmail = true
			break
		}
	}
	if !hasEmail {
		updatedUser.ContactPreferences.PreferredChannels = append(updatedUser.ContactPreferences.PreferredChannels, models.ChannelEmail)
		emailFilter := bson.M{"_id": userObjID}
		emailUpdate := bson.M{"$addToSet": bson.M{"contactPreferences.preferredChannels": models.ChannelEmail}}
		dbService.collectionRefUsers(instanceID).UpdateOne(ctx, emailFilter, emailUpdate)
	}

	return updatedUser, nil
}

// IncrementVerificationCodeAttempts atomically increments the verification code
// attempt counter. The filter ensures the increment only happens if attempts < maxAttempts,
// preventing race conditions between concurrent requests.
// Returns mongo.ErrNoDocuments if the limit has been reached.
func (dbService *UserDBService) IncrementVerificationCodeAttempts(instanceID, userID string, maxAttempts int) (models.User, error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	userObjID, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		return models.User{}, err
	}

	filter := bson.M{
		"_id":                                    userObjID,
		"account.phoneVerificationCode.attempts": bson.M{"$lt": maxAttempts},
	}
	update := bson.M{
		"$inc": bson.M{"account.phoneVerificationCode.attempts": 1},
	}

	var updatedUser models.User
	err = dbService.collectionRefUsers(instanceID).FindOneAndUpdate(
		ctx,
		filter,
		update,
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&updatedUser)

	return updatedUser, err
}
