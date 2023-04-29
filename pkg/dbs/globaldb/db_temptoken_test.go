package globaldb

import (
	"context"
	"testing"
	"time"

	"github.com/coneno/logger"
	"github.com/influenzanet/user-management-service/pkg/models"
	"github.com/influenzanet/user-management-service/pkg/tokens"
	"go.mongodb.org/mongo-driver/bson"
)

// Testing Database Interface methods
func TestDbInterfaceMethodsForTempToken(t *testing.T) {
	testTempToken := models.TempToken{
		UserID:     "test_user_id",
		Purpose:    "test_purpose1",
		InstanceID: testInstanceID,
		Expiration: tokens.GetExpirationTime(10 * time.Second),
	}
	tokenStr := ""

	t.Run("Add temporary token to DB", func(t *testing.T) {
		ts, err := testDBService.AddTempToken(testTempToken)
		if err != nil {
			t.Errorf(err.Error())
			return
		}
		tokenStr = ts

		testTempToken2 := testTempToken
		testTempToken2.Purpose = "test_purpose2"
		_, err = testDBService.AddTempToken(testTempToken2)
		if err != nil {
			t.Errorf(err.Error())
			return
		}
	})

	t.Run("try to get temporary token by wrong token string", func(t *testing.T) {
		tempToken, err := testDBService.GetTempToken(tokenStr + "++")
		if err == nil || tempToken.UserID != "" {
			t.Error(tempToken)
			t.Error("token should not be found")
			return
		}
	})

	t.Run("get temporary token by token string", func(t *testing.T) {
		tempToken, err := testDBService.GetTempToken(tokenStr)
		if err != nil {
			t.Error("token not found by token string")
			return
		}
		if tempToken.UserID != testTempToken.UserID || tempToken.Purpose != testTempToken.Purpose || tempToken.Expiration != testTempToken.Expiration {
			t.Error("temp token does not match")
			t.Error(tempToken)
			return
		}
	})

	t.Run("try to get temporary token by wrong user id", func(t *testing.T) {
		tt, err := testDBService.GetTempTokenForUser(testInstanceID, testTempToken.UserID+"1", "")
		if err != nil {
			t.Error(err)
			return
		}
		if len(tt) > 0 {
			t.Error(tt)
			t.Error("token should not be found")
			return
		}
	})

	t.Run("try to get temporary token by wrong instace id", func(t *testing.T) {
		tt, err := testDBService.GetTempTokenForUser(testInstanceID+"1", testTempToken.UserID, "")
		if err != nil {
			t.Error(err)
			return
		}
		if len(tt) > 0 {
			t.Error(tt)
			t.Error("token should not be found")
			return
		}
	})

	t.Run("try to get temporary token by wrong purpose", func(t *testing.T) {
		tt, err := testDBService.GetTempTokenForUser(testInstanceID, testTempToken.UserID, testTempToken.Purpose+"1")
		if err != nil {
			t.Error(err)
			return
		}
		if len(tt) > 0 {
			t.Error(tt)
			t.Error("token should not be found")
			return
		}
	})

	t.Run("get temporary token by user_id+instance_id", func(t *testing.T) {
		tt, err := testDBService.GetTempTokenForUser(testInstanceID, testTempToken.UserID, "")
		if err != nil {
			t.Error(err)
			return
		}
		logger.Debug.Println(tt)
		if len(tt) < 2 {
			t.Error("tokens should be found")
			return
		}
	})

	t.Run("get temporary token by user_id+instance_id+purpose", func(t *testing.T) {
		tt, err := testDBService.GetTempTokenForUser(testInstanceID, testTempToken.UserID, testTempToken.Purpose)
		if err != nil {
			t.Error(err)
			return
		}
		if len(tt) > 1 {
			t.Error("only one token should be found")
			return
		}
	})

	t.Run("Try delete not existing temporary token", func(t *testing.T) {
		err := testDBService.DeleteTempToken(tokenStr + "1")
		if err == nil {
			t.Error("doc should not be found")
			return
		}
	})

	t.Run("Delete temporary token", func(t *testing.T) {
		err := testDBService.DeleteTempToken(tokenStr)
		if err != nil {
			t.Error(err)
			return
		}
		_, err = testDBService.GetTempToken(testTempToken.Token)
		if err == nil {
			t.Error("token should be deleted by now")
			return
		}
		_, err = testDBService.AddTempToken(testTempToken)
		if err != nil {
			t.Error(err)
			return
		}
	})

	t.Run("Delete all temporary token of a user_id with empty instance_id", func(t *testing.T) {
		err := testDBService.DeleteAllTempTokenForUser("", testTempToken.UserID, "")
		if err != nil {
			t.Error(err)
			return
		}
	})

	t.Run("Try to delete all temporary token of a user_id with wrong id, correct instance_id", func(t *testing.T) {
		err := testDBService.DeleteAllTempTokenForUser(testTempToken.InstanceID, testTempToken.UserID+"3", "")
		if err != nil {
			t.Error(err)
		}
	})

	t.Run("Delete all temporary token of a user_id+instance_id+purpose", func(t *testing.T) {
		err := testDBService.DeleteAllTempTokenForUser(testTempToken.InstanceID, testTempToken.UserID, testTempToken.Purpose)
		if err != nil {
			t.Error(err)
			return
		}
		tokens, err := testDBService.GetTempTokenForUser(testTempToken.InstanceID, testTempToken.UserID, "")
		if err != nil {
			t.Error(err)
			return
		}
		if len(tokens) != 1 {
			t.Error(tokens)
			t.Error("too many tokens found")
			return
		}
	})

	t.Run("Delete all temporary token of a user_id+instance_id", func(t *testing.T) {
		err := testDBService.DeleteAllTempTokenForUser(testTempToken.InstanceID, testTempToken.UserID, "")
		if err != nil {
			t.Error(err)
			return
		}
		tokens, err := testDBService.GetTempTokenForUser(testTempToken.InstanceID, testTempToken.UserID, "")
		if err != nil {
			t.Error(err)
			return
		}
		if len(tokens) > 0 {
			t.Error(tokens)
			t.Error("too many tokens found")
			return
		}
	})
}

func TestDBDeleteExpiredTempTokens(t *testing.T) {
	testTempTokens := []models.TempToken{
		{Expiration: time.Now().Unix() - 10, Purpose: "purpose1", UserID: "testUID", InstanceID: "testInstance1"},
		{Expiration: time.Now().Unix() - 20, Purpose: "purpose1", UserID: "testUID", InstanceID: "testInstance1"},
		{Expiration: time.Now().Unix() - 10, Purpose: "purpose2", UserID: "testUID", InstanceID: "testInstance1"},
		{Expiration: time.Now().Unix() - 20, Purpose: "purpose2", UserID: "testUID", InstanceID: "testInstance1"},
		{Expiration: time.Now().Unix() - 10, Purpose: "purpose1", UserID: "testUID", InstanceID: "testInstance2"},
		{Expiration: time.Now().Unix() - 20, Purpose: "purpose1", UserID: "testUID", InstanceID: "testInstance2"},
		{Expiration: time.Now().Unix() - 20, Purpose: "purpose3", UserID: "testUID", InstanceID: "testInstance3"},
		{Expiration: time.Now().Unix() - 20, Purpose: "purpose4", UserID: "testUID", InstanceID: "testInstance3"},
	}

	for _, token := range testTempTokens {
		_, err := testDBService.AddTempToken(token)
		if err != nil {
			t.Errorf("unexpected error: %v", err.Error())
			return
		}
	}

	t.Run("Delete expired for single purpose", func(t *testing.T) {
		err := testDBService.DeleteTempTokensExpireBefore("", "purpose1", time.Now().Unix()-15)
		if err != nil {
			t.Errorf("unexpected error: %v", err.Error())
			return
		}

		count, err := testDBService.collectionRefTempToken().CountDocuments(context.TODO(), bson.M{})
		if err != nil {
			t.Errorf("unexpected error: %v", err.Error())
			return
		}
		if count != 6 {
			t.Errorf("unexpected number of tokens found: %d instead of %d", count, 7)
		}
	})

	t.Run("Delete expired for single instance", func(t *testing.T) {
		err := testDBService.DeleteTempTokensExpireBefore("testInstance1", "", time.Now().Unix()-5)
		if err != nil {
			t.Errorf("unexpected error: %v", err.Error())
			return
		}

		count, err := testDBService.collectionRefTempToken().CountDocuments(context.TODO(), bson.M{})
		if err != nil {
			t.Errorf("unexpected error: %v", err.Error())
			return
		}
		if count != 3 {
			t.Errorf("unexpected number of tokens found: %d instead of %d", count, 3)
		}
	})

	t.Run("Delete expired for purpose and instance", func(t *testing.T) {
		err := testDBService.DeleteTempTokensExpireBefore("testInstance3", "purpose3", time.Now().Unix()-5)
		if err != nil {
			t.Errorf("unexpected error: %v", err.Error())
			return
		}

		count, err := testDBService.collectionRefTempToken().CountDocuments(context.TODO(), bson.M{})
		if err != nil {
			t.Errorf("unexpected error: %v", err.Error())
			return
		}
		if count != 2 {
			t.Errorf("unexpected number of tokens found: %d instead of %d", count, 3)
		}
	})

	t.Run("Delete expired everywhere", func(t *testing.T) {
		err := testDBService.DeleteTempTokensExpireBefore("", "", time.Now().Unix()-5)
		if err != nil {
			t.Errorf("unexpected error: %v", err.Error())
			return
		}

		count, err := testDBService.collectionRefTempToken().CountDocuments(context.TODO(), bson.M{})
		if err != nil {
			t.Errorf("unexpected error: %v", err.Error())
			return
		}
		if count != 0 {
			t.Errorf("unexpected number of tokens found: %d instead of %d", count, 3)
		}
	})
}
