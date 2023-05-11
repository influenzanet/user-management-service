package userdb

import (
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

// TODO: create index for renewTokens collection
// index for RenewToken (unique)
// index for ExpiresAt
// index for UserID and ExpiresAt and RenewToken

// TODO: remove renew token object by token

// TODO: revoke all tokens for a user

// TODO: remove all expired tokens

// TODO: create new renew token object

// TODO: conditionally update renew token object

type RenewToken struct {
	UserID     string `bson:"userID"`
	RenewToken string `bson:"renewToken"`
	ExpiresAt  int64  `bson:"expiresAt"`
	NextToken  string `bson:"nextToken"` // token that replaces the current renew token
}
