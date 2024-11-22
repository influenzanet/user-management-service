package userdb

import (
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	RENEW_TOKEN_GRACE_PERIOD     = 30 // seconds
	RENEW_TOKEN_DEFAULT_LIFETIME = 60 * 60 * 24 * 90
)

func (dbService *UserDBService) CreateIndexForRenewTokens(instanceID string) error {
	ctx, cancel := dbService.getContext()
	defer cancel()

	_, err := dbService.collectionRenewTokens(instanceID).Indexes().CreateMany(
		ctx, []mongo.IndexModel{
			{
				Keys: bson.D{
					{Key: "userID", Value: 1},
					{Key: "renewToken", Value: 1},
					{Key: "expiresAt", Value: 1},
				},
			},
			{
				Keys: bson.D{
					{Key: "expiresAt", Value: 1},
				},
			},
			{
				Keys: bson.D{
					{Key: "renewToken", Value: 1},
				},
				Options: options.Index().SetUnique(true),
			},
		},
	)
	return err
}

func (dbService *UserDBService) DeleteRenewTokenByToken(instanceID string, token string) error {
	filter := bson.M{"renewToken": token}

	ctx, cancel := dbService.getContext()
	defer cancel()
	res, err := dbService.collectionRenewTokens(instanceID).DeleteOne(ctx, filter, nil)
	if err != nil {
		return err
	}
	if res.DeletedCount < 1 {
		return errors.New("no renew token oject found with the given token value")
	}
	return nil
}

func (dbService *UserDBService) DeleteRenewTokensForUser(instanceID string, userID string) (int64, error) {
	filter := bson.M{"userID": userID}

	ctx, cancel := dbService.getContext()
	defer cancel()
	res, err := dbService.collectionRenewTokens(instanceID).DeleteMany(ctx, filter, nil)
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

func (dbService *UserDBService) DeleteExpiredRenewTokens(instanceID string) (int64, error) {
	filter := bson.M{"expiresAt": bson.M{"$lt": time.Now().Unix()}}

	ctx, cancel := dbService.getContext()
	defer cancel()
	res, err := dbService.collectionRenewTokens(instanceID).DeleteMany(ctx, filter, nil)
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

func (dbService *UserDBService) CreateRenewToken(instanceID string, userID string, renewToken string, expiresAt int64) error {
	ctx, cancel := dbService.getContext()
	defer cancel()

	_, err := dbService.collectionRenewTokens(instanceID).InsertOne(ctx, bson.M{
		"userID":     userID,
		"renewToken": renewToken,
		"expiresAt":  expiresAt,
	})
	return err
}

func (dbService *UserDBService) FindAndUpdateRenewToken(instanceID string, userID string, renewToken string, nextToken string) (rtObj RenewToken, err error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	filter := bson.M{"userID": userID, "renewToken": renewToken, "expiresAt": bson.M{"$gt": time.Now().Unix()}}
	updatePipeline := bson.A{
		bson.M{
			"$set": bson.M{
				"nextToken": bson.M{
					"$cond": bson.A{
						bson.M{
							"$eq": bson.A{
								bson.M{"$ifNull": bson.A{"$nextToken", nil}},
								nil,
							},
						},
						nextToken,
						"$nextToken",
					},
				},
				"expiresAt": bson.M{
					"$cond": bson.A{
						bson.M{
							"$eq": bson.A{
								bson.M{"$ifNull": bson.A{"$nextToken", nil}},
								nil,
							},
						},
						time.Now().Unix() + RENEW_TOKEN_GRACE_PERIOD,
						"$expiresAt",
					},
				},
			},
		},
	}

	opts := options.FindOneAndUpdate().SetReturnDocument(options.After)

	err = dbService.collectionRenewTokens(instanceID).FindOneAndUpdate(ctx, filter, updatePipeline, opts).Decode(&rtObj)
	return
}

type RenewToken struct {
	UserID     string `bson:"userID"`
	RenewToken string `bson:"renewToken"`
	ExpiresAt  int64  `bson:"expiresAt"`
	NextToken  string `bson:"nextToken"` // token that replaces the current renew token
}
