package userdb

import (
	"errors"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
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

// TODO: remove all expired tokens

// TODO: create new renew token object

// TODO: conditionally update renew token object

type RenewToken struct {
	UserID     string `bson:"userID"`
	RenewToken string `bson:"renewToken"`
	ExpiresAt  int64  `bson:"expiresAt"`
	NextToken  string `bson:"nextToken"` // token that replaces the current renew token
}
