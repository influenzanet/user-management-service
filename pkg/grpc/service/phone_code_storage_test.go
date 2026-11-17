package service

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/dbs/userdb"
	"github.com/influenzanet/user-management-service/pkg/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The phone verification code is stored as a keyed digest, never in clear: a copy of the
// database must not yield a code that still works.

var phoneCodeStoragePhoneSeq int64

func phoneCodeStoragePhone() string {
	return fmt.Sprintf("+3932309%04d", atomic.AddInt64(&phoneCodeStoragePhoneSeq, 1))
}

func usersCollection() *mongo.Collection {
	return testUserDBService.DBClient.Database(testUserDBService.DBNamePrefix + testInstanceID + "_users").Collection(userdb.UserCollection)
}

// rawUser reads the account as it is stored, without the model in between, so a field the model
// does not know about is seen too.
func rawUser(t *testing.T, userID string) bson.M {
	t.Helper()
	id, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		t.Fatal(err)
	}
	var doc bson.M
	if err := usersCollection().FindOne(context.Background(), bson.M{"_id": id}).Decode(&doc); err != nil {
		t.Fatalf("cannot read the stored account: %v", err)
	}
	return doc
}

// containsString reports whether value occurs as a string anywhere in the document.
func containsString(doc interface{}, value string) bool {
	switch v := doc.(type) {
	case string:
		return v == value
	case bson.M:
		for _, field := range v {
			if containsString(field, value) {
				return true
			}
		}
	case bson.D:
		for _, field := range v {
			if containsString(field.Value, value) {
				return true
			}
		}
	case bson.A:
		for _, item := range v {
			if containsString(item, value) {
				return true
			}
		}
	}
	return false
}

// requireOnlyDigestStored checks the stored account after a code was sent: the pending code has
// no clear "code" field, its digest is the one of the code the participant received, and that
// code appears nowhere in the document.
func requireOnlyDigestStored(t *testing.T, s *userManagementServer, userID string) {
	t.Helper()
	code := sentPhoneCode(t, s, userID)
	doc := rawUser(t, userID)
	account, _ := doc["account"].(bson.M)
	pending, ok := account["phoneVerificationCode"].(bson.M)
	if !ok {
		t.Fatalf("no pending phone code stored: %v", account)
	}
	if _, found := pending["code"]; found {
		t.Errorf("the pending code has a clear code field: %v", pending)
	}
	if pending["codeHash"] != phoneCodeDigestOf(t, userID, code) {
		t.Errorf("the stored digest is not the one of the code sent: %v", pending)
	}
	if containsString(doc, code) {
		t.Errorf("the code sent (%s) is stored in clear in the account: %v", code, doc)
	}
}

func TestPhoneCodeIsStoredOnlyAsDigest(t *testing.T) {
	t.Run("add phone", func(t *testing.T) {
		s := newPhoneTestServer(&countingWhatsAppClient{})
		token := addRateLimitTestUser(t, "code_digest_add@test.com", nil)
		if _, err := s.AddPhoneNumber(context.Background(), &api.PhoneMsg{Token: &token, NewPhone: phoneCodeStoragePhone()}); err != nil {
			t.Fatal(err)
		}
		requireOnlyDigestStored(t, &s, token.Id)
	})

	t.Run("change phone", func(t *testing.T) {
		s := newPhoneTestServer(&countingWhatsAppClient{})
		token := addVerifyCodeTestUser(t, "code_digest_edit@test.com", phoneCodeStoragePhone(), clearPhoneCode{}, nil, nil)
		if _, err := s.EditPhoneNumber(context.Background(), &api.PhoneMsg{Token: &token, NewPhone: phoneCodeStoragePhone()}); err != nil {
			t.Fatal(err)
		}
		requireOnlyDigestStored(t, &s, token.Id)
	})

	t.Run("resend", func(t *testing.T) {
		s := newPhoneTestServer(&countingWhatsAppClient{})
		phone := phoneCodeStoragePhone()
		token := addVerifyCodeTestUser(t, "code_digest_resend@test.com", phone, clearPhoneCode{}, nil, nil)
		// Resending is open to confirmed accounts only.
		id, _ := primitive.ObjectIDFromHex(token.Id)
		if _, err := usersCollection().UpdateOne(context.Background(), bson.M{"_id": id},
			bson.M{"$set": bson.M{"account.accountConfirmedAt": time.Now().Unix()}}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ResendContactVerification(context.Background(), &api.ResendContactVerificationReq{
			Token: &token, Type: models.ContactTypePhone, Address: phone,
		}); err != nil {
			t.Fatal(err)
		}
		requireOnlyDigestStored(t, &s, token.Id)
	})

	t.Run("the received code verifies the number", func(t *testing.T) {
		s := newPhoneTestServer(&countingWhatsAppClient{})
		token := addRateLimitTestUser(t, "code_digest_verify@test.com", nil)
		if _, err := s.AddPhoneNumber(context.Background(), &api.PhoneMsg{Token: &token, NewPhone: phoneCodeStoragePhone()}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &token, Code: sentPhoneCode(t, &s, token.Id)}); err != nil {
			t.Fatalf("the code the participant received was refused: %v", err)
		}
		if ci, _ := phoneOf(t, token.Id); ci.ConfirmedAt <= 0 {
			t.Errorf("the number was not confirmed: %+v", ci)
		}
	})
}

// A digest copied from one account onto another matches no code there: it covers the account.
func TestPhoneCodeDigestOfAnotherAccountVerifiesNothing(t *testing.T) {
	s := newPhoneTestServer(&countingWhatsAppClient{})
	phone := phoneCodeStoragePhone()
	donor := primitive.NewObjectID()
	copied := clearPhoneCode{Code: "424242", CreatedAt: time.Now().Unix(), ExpiresAt: time.Now().Unix() + 60, Phone: phone}.stored(t, donor.Hex())

	token := addVerifyCodeTestUser(t, "code_digest_copied@test.com", phone, clearPhoneCode{}, nil, nil)
	id, _ := primitive.ObjectIDFromHex(token.Id)
	if _, err := usersCollection().UpdateOne(context.Background(), bson.M{"_id": id},
		bson.M{"$set": bson.M{"account.phoneVerificationCode": copied}}); err != nil {
		t.Fatal(err)
	}

	_, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &token, Code: "424242"})
	if ok, msg := shouldHaveGrpcErrorStatus(err, "invalid verification code"); !ok {
		t.Error(msg)
	}
	if ci, _ := phoneOf(t, token.Id); ci.ConfirmedAt != 0 {
		t.Errorf("a digest of another account confirmed the number: %+v", ci)
	}
}

// A code stored in clear before codes were digested is not checked against anything: the
// participant is told no verification is in progress, spends no attempt, and asks for a new
// code.
func TestClearPhoneCodeStoredBeforeTheChangeVerifiesNothing(t *testing.T) {
	s := newPhoneTestServer(&countingWhatsAppClient{})
	phone := phoneCodeStoragePhone()
	token := addVerifyCodeTestUser(t, "code_digest_legacy@test.com", phone, clearPhoneCode{}, nil, nil)
	id, _ := primitive.ObjectIDFromHex(token.Id)
	legacy := bson.M{"code": "135790", "attempts": 0, "createdAt": time.Now().Unix(), "expiresAt": time.Now().Unix() + 300, "phone": phone}
	if _, err := usersCollection().UpdateOne(context.Background(), bson.M{"_id": id},
		bson.M{"$set": bson.M{"account.phoneVerificationCode": legacy}}); err != nil {
		t.Fatal(err)
	}

	_, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &token, Code: "135790"})
	if ok, msg := shouldHaveGrpcErrorStatus(err, "no phone verification in progress"); !ok {
		t.Error(msg)
	}
	if ci, _ := phoneOf(t, token.Id); ci.ConfirmedAt != 0 {
		t.Errorf("a clear legacy code confirmed the number: %+v", ci)
	}
	user, err := testUserDBService.GetUserByID(testInstanceID, token.Id)
	if err != nil {
		t.Fatal(err)
	}
	if user.Account.PhoneVerificationCode.Attempts != 0 {
		t.Errorf("the refusal spent %d attempts", user.Account.PhoneVerificationCode.Attempts)
	}
}

// Without JWT_TOKEN_KEY no code can be digested. Nothing is sent and nothing is stored, and a
// participant checking a code loses no attempt to the server's fault.
func TestPhoneCodeWithoutDigestKey(t *testing.T) {
	t.Run("no code is sent", func(t *testing.T) {
		client := &countingWhatsAppClient{}
		s := newPhoneTestServer(client)
		token := addRateLimitTestUser(t, "code_digest_nokey_add@test.com", nil)
		t.Setenv("JWT_TOKEN_KEY", "")

		_, err := s.AddPhoneNumber(context.Background(), &api.PhoneMsg{Token: &token, NewPhone: phoneCodeStoragePhone()})
		if status.Code(err) != codes.Internal {
			t.Errorf("expected Internal, got %v", err)
		}
		if client.sends != 0 {
			t.Errorf("%d codes were sent that could never be checked", client.sends)
		}
		user, _ := testUserDBService.GetUserByID(testInstanceID, token.Id)
		if user.Account.PhoneVerificationCode.CodeHash != "" {
			t.Errorf("a code was stored: %+v", user.Account.PhoneVerificationCode)
		}
	})

	t.Run("checking a code spends no attempt", func(t *testing.T) {
		s := newPhoneTestServer(&countingWhatsAppClient{})
		phone := phoneCodeStoragePhone()
		token := addVerifyCodeTestUser(t, "code_digest_nokey_verify@test.com", phone, validCode("246810"), nil, nil)
		t.Setenv("JWT_TOKEN_KEY", "")

		_, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &token, Code: "246810"})
		if status.Code(err) != codes.Internal {
			t.Errorf("expected Internal, got %v", err)
		}
		user, _ := testUserDBService.GetUserByID(testInstanceID, token.Id)
		if user.Account.PhoneVerificationCode.Attempts != 0 {
			t.Errorf("the server's fault spent %d attempts", user.Account.PhoneVerificationCode.Attempts)
		}
		if ci, _ := phoneOf(t, token.Id); ci.ConfirmedAt != 0 {
			t.Errorf("the number was confirmed: %+v", ci)
		}
	})
}
