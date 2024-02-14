package globaldb

import (
	"errors"

	"github.com/influenzanet/user-management-service/pkg/models"
	"github.com/influenzanet/user-management-service/pkg/tokens"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func (dbService *GlobalDBService) AddTempToken(t models.TempToken) (token string, err error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	t.Token, err = tokens.GenerateUniqueTokenString()
	if err != nil {
		return token, err
	}

	_, err = dbService.collectionRefTempToken().InsertOne(ctx, t)
	if err != nil {
		return token, err
	}
	token = t.Token
	return
}

func (dbService *GlobalDBService) GetTempTokenForUser(instanceID string, uid string, purpose string) (tokens models.TempTokens, err error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	filter := bson.M{"instanceID": instanceID, "userID": uid}
	if len(purpose) > 0 {
		filter["purpose"] = purpose
	}

	cur, err := dbService.collectionRefTempToken().Find(ctx, filter)
	if err != nil {
		return tokens, err
	}
	defer cur.Close(ctx)

	tokens = []models.TempToken{}
	for cur.Next(ctx) {
		var result models.TempToken
		err := cur.Decode(&result)
		if err != nil {
			return tokens, err
		}

		tokens = append(tokens, result)
	}
	if err := cur.Err(); err != nil {
		return tokens, err
	}
	return tokens, nil
}

func (dbService *GlobalDBService) GetTempToken(token string) (models.TempToken, error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	filter := bson.M{"token": token}

	t := models.TempToken{}
	err := dbService.collectionRefTempToken().FindOne(ctx, filter).Decode(&t)
	return t, err
}

func (dbService *GlobalDBService) DeleteTempToken(token string) error {
	ctx, cancel := dbService.getContext()
	defer cancel()

	filter := bson.M{"token": token}
	res, err := dbService.collectionRefTempToken().DeleteOne(ctx, filter)
	if err != nil {
		return err
	}
	if res.DeletedCount < 1 {
		return errors.New("document not found")
	}
	return nil
}

func (dbService *GlobalDBService) DeleteAllTempTokenForUser(instanceID string, userID string, purpose string) error {
	ctx, cancel := dbService.getContext()
	defer cancel()

	filter := bson.M{"instanceID": instanceID, "userID": userID}
	if len(purpose) > 0 {
		filter["purpose"] = purpose
	}
	_, err := dbService.collectionRefTempToken().DeleteMany(ctx, filter)
	if err != nil {
		return err
	}
	return nil
}

func (dbService *GlobalDBService) DeleteTempTokensExpireBefore(instanceID string, purpose string, expiresBefore int64) error {
	ctx, cancel := dbService.getContext()
	defer cancel()

	filter := bson.M{"expiration": bson.M{"$lt": expiresBefore}}
	if len(purpose) > 0 {
		filter["purpose"] = purpose
	}
	if len(instanceID) > 0 {
		filter["instanceID"] = instanceID
	}
	_, err := dbService.collectionRefTempToken().DeleteMany(ctx, filter)
	if err != nil {
		return err
	}
	return nil
}

func (dbService *GlobalDBService) CreateIndexForTempTokens() error {
	ctx, cancel := dbService.getContext()
	defer cancel()

	_, err := dbService.collectionRefTempToken().Indexes().CreateMany(
		ctx, []mongo.IndexModel{
			{
				Keys: bson.D{
					{Key: "expiration", Value: 1},
				},
			},
			{
				Keys: bson.D{
					{Key: "token", Value: 1},
				},
				Options: options.Index().SetUnique(true),
			},
			{
				Keys: bson.D{
					{Key: "userID", Value: 1},
					{Key: "instanceID", Value: 1},
					{Key: "purpose", Value: 1},
				},
			},
		},
	)
	return err
}
