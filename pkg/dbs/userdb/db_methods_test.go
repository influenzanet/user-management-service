package userdb

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/coneno/logger"
	"github.com/influenzanet/user-management-service/pkg/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

var testDBService *UserDBService

const (
	testDBNamePrefix = "TEST_"
)

var (
	testInstanceID = strconv.FormatInt(time.Now().Unix(), 10)
)

func setupTestDBService() {
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
	testDBService = NewUserDBService(
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
	log.Println("Drop test database: userdb package")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := testDBService.DBClient.Database(testDBNamePrefix + testInstanceID + "_users").Drop(ctx)
	if err != nil {
		logger.Error.Fatal(err)
	}
}

// Pre-Test Setup
func TestMain(m *testing.M) {
	setupTestDBService()
	result := m.Run()
	dropTestDB()
	os.Exit(result)
}

// Testing Database Interface methods
func TestDbInterfaceMethods(t *testing.T) {
	testUser := models.User{
		Account: models.Account{
			Type:      "email",
			AccountID: "test@test.com",
			Password:  "testhashedpassword-youcantreadme",
		},
		Roles: []string{"TEST"},
		Timestamps: models.Timestamps{
			CreatedAt: time.Now().Unix(),
		},
	}

	t.Run("Testing create user", func(t *testing.T) {
		id, err := testDBService.AddUser(testInstanceID, testUser)
		if err != nil {
			t.Errorf(err.Error())
			return
		}
		if len(id) == 0 {
			t.Errorf("id is missing")
			return
		}
		_id, _ := primitive.ObjectIDFromHex(id)
		testUser.ID = _id
	})

	t.Run("Testing creating existing user", func(t *testing.T) {
		testUser2 := testUser
		testUser2.Roles = []string{"TEST2"}
		_, err := testDBService.AddUser(testInstanceID, testUser2)
		if err == nil {
			t.Errorf("user already existed, but created again")
			return
		}
		u, e := testDBService.GetUserByAccountID(testInstanceID, testUser2.Account.AccountID)
		if e != nil {
			t.Errorf(e.Error())
			return
		}
		if len(u.Roles) > 0 && u.Roles[0] == "TEST2" {
			t.Error("user should not be updated")
		}
	})

	t.Run("Testing find existing user by id", func(t *testing.T) {
		user, err := testDBService.GetUserByID(testInstanceID, testUser.ID.Hex())
		if err != nil {
			t.Errorf(err.Error())
			return
		}
		if user.Account.AccountID != testUser.Account.AccountID {
			t.Errorf("found user is not matching test user")
			return
		}
	})

	t.Run("Testing find not existing user by id", func(t *testing.T) {
		_, err := testDBService.GetUserByID(testInstanceID, testUser.ID.Hex()+"1")
		if err == nil {
			t.Errorf("user should not be found")
			return
		}
	})

	t.Run("Testing find existing user by email", func(t *testing.T) {
		user, err := testDBService.GetUserByAccountID(testInstanceID, testUser.Account.AccountID)
		if err != nil {
			t.Errorf(err.Error())
			return
		}
		if user.Account.AccountID != testUser.Account.AccountID {
			t.Errorf("found user is not matching test user")
			return
		}
	})

	t.Run("Testing find not existing user by email", func(t *testing.T) {
		_, err := testDBService.GetUserByAccountID(testInstanceID, testUser.Account.AccountID+"1")
		if err == nil {
			t.Errorf("user should not be found")
			return
		}
	})

	t.Run("Testing updating existing user's attributes", func(t *testing.T) {
		testUser.Account.AccountConfirmedAt = time.Now().Unix()
		_, err := testDBService.UpdateUser(testInstanceID, testUser)
		if err != nil {
			t.Errorf(err.Error())
			return
		}
	})

	t.Run("Testing updating not existing user's attributes", func(t *testing.T) {
		testUser.Account.AccountConfirmedAt = time.Now().Unix()
		currentUser := testUser
		wrongID := testUser.ID.Hex()[:len(testUser.ID.Hex())-2] + "00"
		id, err := primitive.ObjectIDFromHex(wrongID)
		if err != nil {
			t.Error(err)
			return
		}
		currentUser.ID = id
		_, err = testDBService.UpdateUser(testInstanceID, currentUser)
		if err == nil {
			t.Errorf("cannot update not existing user")
			return
		}
	})

	t.Run("Testing counting recently added users", func(t *testing.T) {
		count, err := testDBService.CountRecentlyCreatedUsers(testInstanceID, 20)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return

		}
		log.Println(count)
		if count < 1 {
			t.Error("at least one user should be found")
		}
	})

	t.Run("Testing deleting existing user", func(t *testing.T) {
		err := testDBService.DeleteUser(testInstanceID, testUser.ID.Hex())
		if err != nil {
			t.Errorf(err.Error())
			return
		}
	})

	t.Run("Testing deleting not existing user", func(t *testing.T) {
		err := testDBService.DeleteUser(testInstanceID, testUser.ID.Hex()+"1")
		if err == nil {
			t.Errorf("user should not be found - error expected")
			return
		}
	})
}

func TestDbPerformActionForUsers(t *testing.T) {
	testUsers := []models.User{
		{Account: models.Account{AccountID: "1"}},
		{Account: models.Account{AccountID: "2"}},
		{Account: models.Account{AccountID: "3"}},
	}
	for _, u := range testUsers {
		_, err := testDBService.AddUser(testInstanceID, u)
		if err != nil {
			logger.Error.Fatal(err)
		}
	}

	// define callback - create users - test if action is performed
	testCallback := func(instanceID string, user models.User, args ...interface{}) error {
		if len(args) != 2 {
			t.Errorf("unexpected number of args: %d", len(args))
			return errors.New("test failed")
		}
		v, ok := args[0].(int)
		if !ok || v != 2 {
			t.Errorf("unexpected value of args[0]: %v", args[0])
			return errors.New("test failed")
		}
		v2, ok2 := args[1].(string)
		if !ok2 || v2 != "hello" {
			t.Errorf("unexpected value of args[1]: %v", args[1])
			return errors.New("test failed")
		}
		return nil
	}

	ctx := context.TODO()

	err := testDBService.PerfomActionForUsers(
		ctx,
		testInstanceID,
		UserFilter{
			OnlyConfirmed:   false,
			ReminderWeekDay: -1,
		},
		testCallback,
		2,
		"hello",
	)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func AssertNumberOfNonParticipantUsers(instanceID string, count int) error {
	users, err := testDBService.FindNonParticipantUsers(instanceID)
	if err != nil {
		return err
	}
	if len(users) != count {
		return fmt.Errorf("wrong number of users found: %d instead of %d", len(users), count)
	}
	return nil
}

func TestDeleteUnverfiedUsers(t *testing.T) {
	testUsers := []models.User{
		{Account: models.Account{AccountID: "delete_1"}, Roles: []string{"RESEARCHER"}, Timestamps: models.Timestamps{CreatedAt: time.Now().Unix() - 100}},
		{Account: models.Account{AccountID: "delete_2"}, Roles: []string{"RESEARCHER"}, Timestamps: models.Timestamps{CreatedAt: time.Now().Unix() - 50}},
		{Account: models.Account{AccountID: "delete_3"}, Roles: []string{"RESEARCHER"}, Timestamps: models.Timestamps{CreatedAt: time.Now().Unix()}},
	}
	for _, u := range testUsers {
		_, err := testDBService.AddUser(testInstanceID, u)
		if err != nil {
			logger.Error.Fatal(err)
		}
	}

	t.Run("remove any other user not in the test set", func(t *testing.T) {
		count, err := testDBService.DeleteUnverfiedUsers(testInstanceID, time.Now().Unix()-105)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		log.Printf("deleted %d users", count)
		err = AssertNumberOfNonParticipantUsers(testInstanceID, 3)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
	})

	t.Run("remove 1 user", func(t *testing.T) {
		count, err := testDBService.DeleteUnverfiedUsers(testInstanceID, time.Now().Unix()-55)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		log.Printf("deleted %d users", count)
		err = AssertNumberOfNonParticipantUsers(testInstanceID, 2)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
	})

	t.Run("remove an other user", func(t *testing.T) {
		count, err := testDBService.DeleteUnverfiedUsers(testInstanceID, time.Now().Unix()-15)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		log.Printf("deleted %d users", count)
		err = AssertNumberOfNonParticipantUsers(testInstanceID, 1)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
	})
}
