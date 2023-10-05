package userdb

import (
	"context"
	"errors"
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

// low level find and replace
func (dbService *UserDBService) _updateUserInDB(orgID string, user models.User) (models.User, error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	elem := models.User{}
	filter := bson.M{"_id": user.ID}
	rd := options.After
	fro := options.FindOneAndReplaceOptions{
		ReturnDocument: &rd,
	}
	err := dbService.collectionRefUsers(orgID).FindOneAndReplace(ctx, filter, user, &fro).Decode(&elem)
	return elem, err
}

func (dbService *UserDBService) UpdateUser(instanceID string, updatedUser models.User) (models.User, error) {
	// Set last update time
	updatedUser.Timestamps.UpdatedAt = time.Now().Unix()
	return dbService._updateUserInDB(instanceID, updatedUser)
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

	elem := models.User{}

	rd := options.After
	fro := options.FindOneAndUpdateOptions{
		ReturnDocument: &rd,
	}
	update := bson.M{"$set": bson.M{"contactPreferences": prefs, "timestamps.updatedAt": time.Now().Unix()}}
	err := dbService.collectionRefUsers(instanceID).FindOneAndUpdate(ctx, filter, update, &fro).Decode(&elem)
	return elem, err
}

func (dbService *UserDBService) UpdateLoginTime(instanceID string, id string) error {
	ctx, cancel := dbService.getContext()
	defer cancel()

	_id, _ := primitive.ObjectIDFromHex(id)
	filter := bson.M{"_id": _id}
	update := bson.M{"$set": bson.M{"timestamps.lastLogin": time.Now().Unix()}}
	_, err := dbService.collectionRefUsers(instanceID).UpdateOne(ctx, filter, update)
	if err != nil {
		return err
	}
	_, err = dbService.UpdateMarkedForDeletionTime(instanceID, id, 0, true)
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

func (dbService *UserDBService) UpdateMarkedForDeletionTime(instanceID string, id string, dT2 int64, reset bool) (bool, error) {
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
	update := bson.M{"$set": bson.M{"timestamps.markedForDeletion": time.Now().Unix() + dT2}}
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
		bson.M{"timestamps.timestamps.markedForDeletion": bson.M{"$gt": 0}},
		bson.M{"timestamps.timestamps.markedForDeletion": bson.M{"$lt": time.Now().Unix()}},
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

func (dbService *UserDBService) FindInactiveUsers(instanceID string, dT1 int64) (users []models.User, err error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	filter := bson.M{}
	filter["$and"] = bson.A{
		bson.M{ //TODO test if works as expected
			"roles": bson.M{"$nin": bson.A{
				constants.USER_ROLE_SERVICE_ACCOUNT,
				constants.USER_ROLE_RESEARCHER,
				constants.USER_ROLE_ADMIN,
			}},
		},
		bson.M{"timestamps.lastLogin": bson.M{"$lt": time.Now().Unix() - dT1}},
		bson.M{"timestamps.lastTokenRefresh": bson.M{"$lt": time.Now().Unix() - dT1}},
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
					{Key: "roles", Value: 1},
					{Key: "timestamps.lastLogin", Value: 1},
					{Key: "timestamps.lastTokenRefresh", Value: 1},
					{Key: "timestamps.markedForDeletion", Value: 1},
				},
			},
			{
				Keys: bson.D{
					{Key: "timestamps.markedForDeletion", Value: 1},
				},
			},
		},
	)
	return err
}
