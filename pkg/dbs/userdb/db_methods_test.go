package userdb

import (
	"context"
	"errors"
	"fmt"
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
	logger.Info.Println("Drop test database: userdb package")
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
		logger.Debug.Println(count)
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
		logger.Debug.Printf("deleted %d users", count)
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
		logger.Debug.Printf("deleted %d users", count)
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
		logger.Debug.Printf("deleted %d users", count)
		err = AssertNumberOfNonParticipantUsers(testInstanceID, 1)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
	})
}

func TestFindInactiveUsers(t *testing.T) {
	notifyAfter := int64(100)
	deleteAfter := 200
	testUsers := []models.User{
		{Account: models.Account{AccountID: "inactive_1"}, Roles: []string{"PARTICIPANT", "ADMIN"},
			Timestamps: models.Timestamps{CreatedAt: time.Now().Unix() - 100, LastTokenRefresh: time.Now().Unix() - int64(notifyAfter) - 100, LastLogin: time.Now().Unix() - int64(notifyAfter) - 100}},
		{Account: models.Account{AccountID: "inactive_2"}, Roles: []string{"RESEARCHER"},
			Timestamps: models.Timestamps{CreatedAt: time.Now().Unix() - 100, LastTokenRefresh: time.Now().Unix() - int64(notifyAfter) - 100, LastLogin: time.Now().Unix() - int64(notifyAfter) - 100}},
		{Account: models.Account{AccountID: "inactive_3"}, Roles: []string{"PARTICIPANT"},
			Timestamps: models.Timestamps{CreatedAt: time.Now().Unix() - 100, LastTokenRefresh: time.Now().Unix() - int64(notifyAfter) - 100, LastLogin: time.Now().Unix() - int64(notifyAfter) - 100}},
		{Account: models.Account{AccountID: "inactive_4"}, Roles: []string{"PARTICIPANT"},
			Timestamps: models.Timestamps{CreatedAt: time.Now().Unix() - 100, LastTokenRefresh: time.Now().Unix() - int64(notifyAfter) + 10, LastLogin: time.Now().Unix() - int64(notifyAfter) - 100}},
		{Account: models.Account{AccountID: "inactive_5"}, Roles: []string{"PARTICIPANT"},
			Timestamps: models.Timestamps{CreatedAt: time.Now().Unix() - 100, LastTokenRefresh: time.Now().Unix() - int64(notifyAfter) - 100, LastLogin: time.Now().Unix() - int64(notifyAfter) + 10}},
		{Account: models.Account{AccountID: "inactive_6"}, Roles: []string{"PARTICIPANT"},
			Timestamps: models.Timestamps{CreatedAt: time.Now().Unix() - 100, LastTokenRefresh: time.Now().Unix() - int64(notifyAfter) - 100, LastLogin: time.Now().Unix() - int64(notifyAfter) - 100,
				MarkedForDeletion: 0}},
		{Account: models.Account{AccountID: "inactive_7"}, Roles: []string{"PARTICIPANT"},
			Timestamps: models.Timestamps{CreatedAt: time.Now().Unix() - 100, LastTokenRefresh: time.Now().Unix() - int64(notifyAfter) - 100, LastLogin: time.Now().Unix() - int64(notifyAfter) - 100,
				MarkedForDeletion: 100}},
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
		logger.Debug.Printf("deleted %d users", count)
	})

	t.Run("Testing finding inactive users", func(t *testing.T) {
		inactiveUsers, err := testDBService.FindInactiveUsers(testInstanceID, notifyAfter)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if len(inactiveUsers) != 2 {
			t.Errorf("wrong number of inactive users found: %d instead of %d", len(inactiveUsers), 1)
			return
		}
		if inactiveUsers[0].Account.AccountID != "inactive_3" {
			t.Errorf("wrong user found: %s instead of %s", inactiveUsers[0].Account.AccountID, "inactive_3")
			return
		}
		if inactiveUsers[1].Account.AccountID != "inactive_6" {
			t.Errorf("wrong user found: %s instead of %s", inactiveUsers[0].Account.AccountID, "inactive_6")
			return
		}
	})

	t.Run("Testing updating markedForDeletionTime", func(t *testing.T) {

		user1, err := testDBService.GetUserByAccountID(testInstanceID, "inactive_2")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		success, err := testDBService.UpdateMarkedForDeletionTime(testInstanceID, string(user1.ID.Hex()), int64(deleteAfter), false)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if success == false {
			t.Errorf("could not update markedforDeletion Timestamp for user: %s", "inactive_2")
			return
		}
	})

	t.Run("Testing updating and resetting markedForDeletionTime when it is already set", func(t *testing.T) {
		user2, err := testDBService.GetUserByAccountID(testInstanceID, "inactive_7")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		success, err := testDBService.UpdateMarkedForDeletionTime(testInstanceID, string(user2.ID.Hex()), int64(deleteAfter), false)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if success != false {
			t.Errorf("update markedforDeletion timestamp that is already set for user: %s", "inactive_7")
			return
		}
		success, err = testDBService.UpdateMarkedForDeletionTime(testInstanceID, user2.ID.Hex(), int64(deleteAfter), true)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if success == false {
			t.Errorf("resetting markedforDeletion timestamp for user failed: %s", "inactive_7")
			return
		}
		updatedUser, err := testDBService.GetUserByID(testInstanceID, user2.ID.Hex())
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if updatedUser.Timestamps.MarkedForDeletion != 0 {
			t.Errorf("unexpected markedforDeletion timestamp for user: %s should be zero, but it is %d", "inactive_7", updatedUser.Timestamps.MarkedForDeletion)
			return
		}
	})

	t.Run("Testing finding users marked for deletion", func(t *testing.T) {
		users, err := testDBService.FindUsersMarkedForDeletion(testInstanceID)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if len(users) != 0 {
			t.Errorf("wrong number of inactive users found: %d instead of %d", len(users), 0)
			return
		}
		//add User
		testUser := models.User{
			Account: models.Account{AccountID: "inactive_8"},
			Roles:   []string{"PARTICIPANT"},
			Timestamps: models.Timestamps{
				CreatedAt:         time.Now().Unix() - 100,
				MarkedForDeletion: time.Now().Unix() - 10}}

		id, err := testDBService.AddUser(testInstanceID, testUser)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if len(id) == 0 {
			t.Errorf("id is missing")
			return
		}
		_id, _ := primitive.ObjectIDFromHex(id)
		testUser.ID = _id

		users, err = testDBService.FindUsersMarkedForDeletion(testInstanceID)
		if len(users) != 1 {
			t.Errorf("wrong number of inactive users found: %d instead of %d", len(users), 1)
			return
		}
		success, err := testDBService.UpdateMarkedForDeletionTime(testInstanceID, id, 0, true)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if success != true {
			t.Errorf("could not reset MarkedForDeletionTime: %v", err)
			return
		}
		users, err = testDBService.FindUsersMarkedForDeletion(testInstanceID)
		if len(users) != 0 {
			t.Errorf("wrong number of inactive users found: %d instead of %d", len(users), 0)
			return
		}
	})

	t.Run("Testing update Login Time", func(t *testing.T) {
		users, err := testDBService.FindInactiveUsers(testInstanceID, notifyAfter)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if len(users) != 4 {
			t.Errorf("wrong number of inactive users found: %d instead of %d", len(users), 4)
			return
		}
		id := users[0].ID.Hex()
		testDBService.UpdateLoginTime(testInstanceID, id)
		users, err = testDBService.FindInactiveUsers(testInstanceID, notifyAfter)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if len(users) != 3 {
			t.Errorf("wrong number of inactive users found: %d instead of %d", len(users), 3)
			return
		}
	})
}

func addReserveTestUser(t *testing.T, accountID string, attempts []int64, contactInfos []models.ContactInfo) string {
	t.Helper()
	testUser := models.User{
		Account: models.Account{
			Type:                      "email",
			AccountID:                 accountID,
			Password:                  "testhashedpassword-youcantreadme",
			PhoneVerificationAttempts: attempts,
		},
		ContactInfos: contactInfos,
		Timestamps: models.Timestamps{
			CreatedAt: time.Now().Unix(),
		},
	}
	id, err := testDBService.AddUser(testInstanceID, testUser)
	if err != nil {
		t.Fatalf(err.Error())
	}
	return id
}

func TestDbReservePhoneVerificationSlot(t *testing.T) {
	const maxAttempts = 3
	const window = int64(300)

	t.Run("with attempts field stored as null (legacy user)", func(t *testing.T) {
		id := addReserveTestUser(t, "reserve_slot_legacy@test.com", nil, nil)
		ok, err := testDBService.ReservePhoneVerificationSlot(testInstanceID, id, maxAttempts, window)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if !ok {
			t.Error("expected a free slot for a fresh user")
		}
		user, err := testDBService.GetUserByID(testInstanceID, id)
		if err != nil {
			t.Errorf(err.Error())
			return
		}
		if len(user.Account.PhoneVerificationAttempts) != 1 {
			t.Errorf("wrong number of attempts: %d instead of %d", len(user.Account.PhoneVerificationAttempts), 1)
		}
	})

	t.Run("consumes slots up to the limit then rejects", func(t *testing.T) {
		id := addReserveTestUser(t, "reserve_slot_limit@test.com", []int64{}, nil)
		for i := 0; i < maxAttempts; i++ {
			ok, err := testDBService.ReservePhoneVerificationSlot(testInstanceID, id, maxAttempts, window)
			if err != nil {
				t.Errorf("unexpected error on reserve #%d: %v", i+1, err)
				return
			}
			if !ok {
				t.Errorf("reserve #%d should have succeeded", i+1)
				return
			}
		}
		ok, err := testDBService.ReservePhoneVerificationSlot(testInstanceID, id, maxAttempts, window)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if ok {
			t.Errorf("reserve #%d should have been rejected", maxAttempts+1)
		}
		user, err := testDBService.GetUserByID(testInstanceID, id)
		if err != nil {
			t.Errorf(err.Error())
			return
		}
		if len(user.Account.PhoneVerificationAttempts) != maxAttempts {
			t.Errorf("wrong number of attempts: %d instead of %d", len(user.Account.PhoneVerificationAttempts), maxAttempts)
		}
	})

	t.Run("attempts outside the window free their slots", func(t *testing.T) {
		old := time.Now().Unix() - window - 10
		id := addReserveTestUser(t, "reserve_slot_expired@test.com", []int64{old, old, old}, nil)
		ok, err := testDBService.ReservePhoneVerificationSlot(testInstanceID, id, maxAttempts, window)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if !ok {
			t.Error("expected a free slot when all recorded attempts are outside the window")
		}
		// Freeing a slot must also prune it: expired attempts are dropped by the same
		// pipeline update, so the array cannot grow beyond the window's worth of entries.
		user, err := testDBService.GetUserByID(testInstanceID, id)
		if err != nil {
			t.Errorf(err.Error())
			return
		}
		if len(user.Account.PhoneVerificationAttempts) != 1 {
			t.Errorf("expired attempts were not pruned: %d entries instead of 1 (%v)",
				len(user.Account.PhoneVerificationAttempts), user.Account.PhoneVerificationAttempts)
			return
		}
		if user.Account.PhoneVerificationAttempts[0] <= old {
			t.Errorf("surviving entry should be the new attempt, got %d", user.Account.PhoneVerificationAttempts[0])
		}
	})

	t.Run("unknown user matches no document", func(t *testing.T) {
		ok, err := testDBService.ReservePhoneVerificationSlot(testInstanceID, primitive.NewObjectID().Hex(), maxAttempts, window)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if ok {
			t.Error("expected no reservation for an unknown user")
		}
	})
}

func TestDbSetPhoneVerificationCode(t *testing.T) {
	t.Run("sets the code without touching recorded attempts", func(t *testing.T) {
		now := time.Now().Unix()
		id := addReserveTestUser(t, "set_phone_code@test.com", []int64{now - 1, now - 2}, nil)
		code := models.VerificationCode{Code: "123456", Attempts: 0, CreatedAt: now, ExpiresAt: now + 60}
		if err := testDBService.SetPhoneVerificationCode(testInstanceID, id, code); err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		user, err := testDBService.GetUserByID(testInstanceID, id)
		if err != nil {
			t.Errorf(err.Error())
			return
		}
		if user.Account.PhoneVerificationCode.Code != "123456" {
			t.Errorf("code not persisted: %+v", user.Account.PhoneVerificationCode)
		}
		if len(user.Account.PhoneVerificationAttempts) != 2 {
			t.Errorf("recorded attempts were touched: %d instead of 2", len(user.Account.PhoneVerificationAttempts))
		}
	})
}

func TestDbAddPhoneContactInfoIfAbsent(t *testing.T) {
	newPhoneCI := func(phone string) models.ContactInfo {
		return models.ContactInfo{
			ID:    primitive.NewObjectID(),
			Type:  models.ContactTypePhone,
			Phone: phone,
		}
	}

	t.Run("adds the phone when the user has none", func(t *testing.T) {
		emailCI := models.ContactInfo{
			ID: primitive.NewObjectID(), Type: models.ContactTypeEmail,
			Email: "add_ci_absent@test.com", ConfirmedAt: time.Now().Unix(),
		}
		id := addReserveTestUser(t, "add_ci_absent@test.com", []int64{}, []models.ContactInfo{emailCI})
		ok, err := testDBService.AddPhoneContactInfoIfAbsent(testInstanceID, id, newPhoneCI("+391230000001"))
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if !ok {
			t.Error("expected the phone to be added")
		}
		user, _ := testDBService.GetUserByID(testInstanceID, id)
		phones := 0
		for _, ci := range user.ContactInfos {
			if ci.Type == models.ContactTypePhone {
				phones++
			}
		}
		if phones != 1 {
			t.Errorf("expected exactly 1 phone contact info, got %d", phones)
		}
	})

	t.Run("refuses when a phone already exists", func(t *testing.T) {
		id := addReserveTestUser(t, "add_ci_present@test.com", []int64{}, []models.ContactInfo{
			{ID: primitive.NewObjectID(), Type: models.ContactTypePhone, Phone: "+391230000002"},
		})
		ok, err := testDBService.AddPhoneContactInfoIfAbsent(testInstanceID, id, newPhoneCI("+391230000003"))
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if ok {
			t.Error("expected the add to be refused when a phone contact info exists")
		}
		user, _ := testDBService.GetUserByID(testInstanceID, id)
		phones := 0
		for _, ci := range user.ContactInfos {
			if ci.Type == models.ContactTypePhone {
				phones++
			}
		}
		if phones != 1 {
			t.Errorf("expected exactly 1 phone contact info, got %d", phones)
		}
	})
}

func TestDbReplacePhoneContactInfo(t *testing.T) {
	t.Run("replaces the phone entry in place and keeps others", func(t *testing.T) {
		emailCI := models.ContactInfo{
			ID: primitive.NewObjectID(), Type: models.ContactTypeEmail,
			Email: "replace_ci@test.com", ConfirmedAt: time.Now().Unix(),
		}
		oldPhone := models.ContactInfo{
			ID: primitive.NewObjectID(), Type: models.ContactTypePhone,
			Phone: "+391230000004", ConfirmedAt: time.Now().Unix(),
		}
		id := addReserveTestUser(t, "replace_ci@test.com", []int64{}, []models.ContactInfo{emailCI, oldPhone})
		newCI := models.ContactInfo{
			ID: primitive.NewObjectID(), Type: models.ContactTypePhone, Phone: "+391230000005",
		}
		if err := testDBService.ReplacePhoneContactInfo(testInstanceID, id, newCI); err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		user, _ := testDBService.GetUserByID(testInstanceID, id)
		var phone *models.ContactInfo
		emails := 0
		for i, ci := range user.ContactInfos {
			switch ci.Type {
			case models.ContactTypePhone:
				phone = &user.ContactInfos[i]
			case models.ContactTypeEmail:
				emails++
			}
		}
		if phone == nil || phone.Phone != "+391230000005" || phone.ConfirmedAt != 0 {
			t.Errorf("phone contact info not replaced correctly: %+v", phone)
		}
		if emails != 1 {
			t.Errorf("email contact info lost: %d", emails)
		}
	})
}

func TestDbSetPhoneVerificationSentAt(t *testing.T) {
	t.Run("stamps only the matching phone", func(t *testing.T) {
		id := addReserveTestUser(t, "sent_at_ci@test.com", []int64{}, []models.ContactInfo{
			{ID: primitive.NewObjectID(), Type: models.ContactTypePhone, Phone: "+391230000006"},
		})
		ts := time.Now().Unix()
		if err := testDBService.SetPhoneVerificationSentAt(testInstanceID, id, "+391230000006", ts); err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		user, _ := testDBService.GetUserByID(testInstanceID, id)
		for _, ci := range user.ContactInfos {
			if ci.Type == models.ContactTypePhone && ci.ConfirmationLinkSentAt != ts {
				t.Errorf("confirmationLinkSentAt not stamped: %d", ci.ConfirmationLinkSentAt)
			}
		}
	})
}
