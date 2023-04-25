package service

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/coneno/logger"
	"github.com/influenzanet/user-management-service/pkg/dbs/globaldb"
	"github.com/influenzanet/user-management-service/pkg/dbs/userdb"
	"github.com/influenzanet/user-management-service/pkg/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"google.golang.org/grpc/status"
)

var testGlobalDBService *globaldb.GlobalDBService
var testUserDBService *userdb.UserDBService

const (
	testDBNamePrefix = "TEST_SERVICE_"
)

var (
	testInstanceID = strconv.FormatInt(time.Now().Unix(), 10)
)

// Pre-Test Setup
func TestMain(m *testing.M) {
	setupTestGlobalDBService()
	setupTestUserDBService()
	result := m.Run()
	dropTestDB()
	os.Exit(result)
}

func setupTestGlobalDBService() {
	connStr := os.Getenv("GLOBAL_DB_CONNECTION_STR")
	username := os.Getenv("GLOBAL_DB_USERNAME")
	password := os.Getenv("GLOBAL_DB_PASSWORD")
	prefix := os.Getenv("GLOBAL_DB_CONNECTION_PREFIX") // Used in test mode
	if connStr == "" || username == "" || password == "" {
		logger.Error.Fatal("Couldn't read DB credentials.")
	}
	URI := fmt.Sprintf(`mongodb%s://%s:%s@%s`, prefix, username, password, connStr)

	var err error
	Timeout, err := strconv.Atoi(os.Getenv("DB_TIMEOUT"))
	if err != nil {
		logger.Error.Fatal("DB_TIMEOUT: " + err.Error())
	}
	IdleConnTimeout, err := strconv.Atoi(os.Getenv("DB_IDLE_CONN_TIMEOUT"))
	if err != nil {
		logger.Error.Fatal("DB_IDLE_CONN_TIMEOUT" + err.Error())
	}
	mps, err := strconv.Atoi(os.Getenv("DB_MAX_POOL_SIZE"))
	MaxPoolSize := uint64(mps)
	if err != nil {
		logger.Error.Fatal("DB_MAX_POOL_SIZE: " + err.Error())
	}
	testGlobalDBService = globaldb.NewGlobalDBService(
		models.DBConfig{
			URI:             URI,
			Timeout:         Timeout,
			IdleConnTimeout: IdleConnTimeout,
			MaxPoolSize:     MaxPoolSize,
			DBNamePrefix:    testDBNamePrefix,
		},
	)
}

func setupTestUserDBService() {
	connStr := os.Getenv("USER_DB_CONNECTION_STR")
	username := os.Getenv("USER_DB_USERNAME")
	password := os.Getenv("USER_DB_PASSWORD")
	prefix := os.Getenv("USER_DB_CONNECTION_PREFIX") // Used in test mode
	if connStr == "" || username == "" || password == "" {
		logger.Error.Fatal("Couldn't read DB credentials.")
	}
	URI := fmt.Sprintf(`mongodb%s://%s:%s@%s`, prefix, username, password, connStr)

	var err error
	Timeout, err := strconv.Atoi(os.Getenv("DB_TIMEOUT"))
	if err != nil {
		logger.Error.Fatal("DB_TIMEOUT: " + err.Error())
	}
	IdleConnTimeout, err := strconv.Atoi(os.Getenv("DB_IDLE_CONN_TIMEOUT"))
	if err != nil {
		logger.Error.Fatal("DB_IDLE_CONN_TIMEOUT" + err.Error())
	}
	mps, err := strconv.Atoi(os.Getenv("DB_MAX_POOL_SIZE"))
	MaxPoolSize := uint64(mps)
	if err != nil {
		logger.Error.Fatal("DB_MAX_POOL_SIZE: " + err.Error())
	}
	testUserDBService = userdb.NewUserDBService(
		models.DBConfig{
			URI:             URI,
			Timeout:         Timeout,
			IdleConnTimeout: IdleConnTimeout,
			MaxPoolSize:     MaxPoolSize,
			DBNamePrefix:    testDBNamePrefix,
		},
	)
}

func dropTestDB() {
	logger.Info.Println("Drop test database: service package")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := testUserDBService.DBClient.Database(testDBNamePrefix + testInstanceID + "_users").Drop(ctx)
	if err != nil {
		logger.Error.Fatal(err)
	}
	err = testGlobalDBService.DBClient.Database(testDBNamePrefix + "global-infos").Drop(ctx)
	if err != nil {
		logger.Error.Fatal(err)
	}
}

func shouldHaveGrpcErrorStatus(err error, expectedError string) (bool, string) {
	if err == nil {
		return false, "should return an error"
	}
	st, ok := status.FromError(err)
	if !ok || st == nil {
		return false, fmt.Sprintf("unexpected error: %s", err.Error())
	}

	if len(expectedError) > 0 && st.Message() != expectedError {
		return false, fmt.Sprintf("wrong error: %s", st.Message())
	}
	return true, ""
}

func addTestUsers(userDefs []models.User) (users []models.User, err error) {
	for _, uc := range userDefs {
		ID, err := testUserDBService.AddUser(testInstanceID, uc)
		if err != nil {
			return users, err
		}
		_id, _ := primitive.ObjectIDFromHex(ID)
		uc.ID = _id
		users = append(users, uc)
	}
	return
}
