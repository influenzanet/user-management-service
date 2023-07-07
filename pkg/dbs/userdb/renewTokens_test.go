package userdb

import (
	"testing"
	"time"

	"github.com/coneno/logger"
)

func TestRenewTokenDBMethods(t *testing.T) {
	testToken := RenewToken{
		ExpiresAt:  time.Now().Unix() + 1000,
		RenewToken: "TEST_RENEW_TOKEN",
		UserID:     "TEST_USER_ID",
	}

	logger.Debug.Println(testToken)

	t.Run("Testing create token", func(t *testing.T) {
		err := testDBService.CreateRenewToken(testInstanceID, testToken.UserID, testToken.RenewToken, testToken.ExpiresAt)
		if err != nil {
			t.Errorf(err.Error())
			return
		}
	})

	firstNextToken := "FIRST_NEXT_TOKEN"
	secondNextToken := "SECOND_NEXT_TOKEN"
	t.Run("Testing conditional update with empty nextToken", func(t *testing.T) {
		rt, err := testDBService.FindAndUpdateRenewToken(testInstanceID, testToken.UserID, testToken.RenewToken, firstNextToken)
		if err != nil {
			t.Errorf(err.Error())
			return
		}
		logger.Debug.Println(rt)
		if rt.RenewToken != testToken.RenewToken {
			t.Errorf("renew token does not match")
			return
		}
		if rt.NextToken != firstNextToken {
			t.Errorf("next token does not match")
			return
		}
	})

	t.Run("Testing conditional update with non empty nextToken", func(t *testing.T) {
		rt, err := testDBService.FindAndUpdateRenewToken(testInstanceID, testToken.UserID, testToken.RenewToken, secondNextToken)
		if err != nil {
			t.Errorf(err.Error())
			return
		}
		logger.Debug.Println(rt)
		if rt.RenewToken != testToken.RenewToken {
			t.Errorf("renew token does not match")
			return
		}
		if rt.NextToken == secondNextToken {
			t.Errorf("next token should not match with second next token value")
			return
		}
		if rt.NextToken != firstNextToken {
			t.Errorf("next token should match with first next token")
			return
		}
	})

}
