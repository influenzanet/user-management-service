package userdb

import (
	"context"
	"time"

	"github.com/coneno/logger"
	"github.com/influenzanet/user-management-service/pkg/models"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const UserCollection = "users"
const RenewTokenCollection = "renewTokens"

type UserDBService struct {
	DBClient        *mongo.Client
	timeout         int
	noCursorTimeout bool
	DBNamePrefix    string
}

func NewUserDBService(configs models.DBConfig) *UserDBService {
	var err error
	dbClient, err := mongo.NewClient(
		options.Client().ApplyURI(configs.URI),
		options.Client().SetMaxConnIdleTime(time.Duration(configs.IdleConnTimeout)*time.Second),
		options.Client().SetMaxPoolSize(configs.MaxPoolSize),
	)
	if err != nil {
		logger.Error.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(configs.Timeout)*time.Second)
	defer cancel()

	err = dbClient.Connect(ctx)
	if err != nil {
		logger.Error.Fatal(err)
	}

	ctx, conCancel := context.WithTimeout(context.Background(), time.Duration(configs.Timeout)*time.Second)
	err = dbClient.Ping(ctx, nil)
	defer conCancel()
	if err != nil {
		logger.Error.Fatal("fail to connect to DB: " + err.Error())
	}

	return &UserDBService{
		DBClient:        dbClient,
		timeout:         configs.Timeout,
		noCursorTimeout: configs.NoCursorTimeout,
		DBNamePrefix:    configs.DBNamePrefix,
	}
}

// Collections
func (dbService *UserDBService) collectionRefUsers(instanceID string) *mongo.Collection {
	return dbService.DBClient.Database(dbService.DBNamePrefix + instanceID + "_users").Collection(UserCollection)
}

// collectionRenewTokens get collection for RenewTokens
func (dbSerive *UserDBService) collectionRenewTokens(instanceID string) *mongo.Collection {
	return dbSerive.DBClient.Database(dbSerive.DBNamePrefix + instanceID + "_users").Collection(RenewTokenCollection)
}

// DB utils
func (dbService *UserDBService) getContext() (ctx context.Context, cancel context.CancelFunc) {
	return context.WithTimeout(context.Background(), time.Duration(dbService.timeout)*time.Second)
}

func (dbService *UserDBService) GetTimeout() time.Duration {
	return time.Duration(dbService.timeout) * time.Second
}

// Public version of getContext
func (dbService *UserDBService) GetContext() (ctx context.Context, cancel context.CancelFunc) {
	return context.WithTimeout(context.Background(), time.Duration(dbService.timeout)*time.Second)
}

// GetCollection from userDb service.
// Generic public function to be useable in migration scripts
func (dbService *UserDBService) GetCollection(instanceID string, name string) *mongo.Collection {
	return dbService.DBClient.Database(dbService.DBNamePrefix + instanceID + "_users").Collection(name)
}
