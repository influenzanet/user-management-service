package service

import (
	"context"
	"testing"
	"time"

	api_types "github.com/influenzanet/go-utils/pkg/api_types"
	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// F-02: the verification code was stored without the number it had been sent to, so it
// confirmed whichever unverified phone the account carried when it was used. A participant who
// asked for a code for one number and then changed the number before using it could confirm a
// number nobody ever proved control of, and the whatsapp channel followed.
//
// The document these tests start from is the one that interleaving leaves behind: the account
// now carries the second number, unverified, while the pending code belongs to the first.

const (
	codeSentTo      = "+391230000801"
	codeNowOnPhone  = "+391230000802"
	foreignCodeText = "123456"
)

// addForeignCodeUser seeds the post-race document directly: the stored code carries the number
// it was sent to, which is no longer the number on the account.
func addForeignCodeUser(t *testing.T, accountID string, storedPhone string, channels []string) api_types.TokenInfos {
	t.Helper()
	code := validCode(foreignCodeText)
	code.Phone = storedPhone
	return addVerifyCodeTestUser(t, accountID, codeNowOnPhone, code, channels, nil)
}

func TestVerifyWhatsAppCodeRejectsCodeSentToAnotherNumber(t *testing.T) {
	s := newRateLimitTestServer(&countingWhatsAppClient{})
	s.Intervals.MaxVerificationAttempts = 3

	t.Run("a code sent to a number the account no longer has confirms nothing", func(t *testing.T) {
		token := addForeignCodeUser(t, "binding_foreign@test.com", codeSentTo, nil)

		_, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &token, Code: foreignCodeText})
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("expected InvalidArgument, got %v", err)
		}

		phone, found := phoneOf(t, token.Id)
		if !found {
			t.Fatal("the current number disappeared")
		}
		if phone.ConfirmedAt != 0 {
			t.Errorf("a code sent to %s confirmed %s: %+v", codeSentTo, codeNowOnPhone, phone)
		}
		if hasChannel(channelsOf(t, token.Id), models.ChannelWhatsApp) {
			t.Errorf("whatsapp was enabled for an unproven number: %v", channelsOf(t, token.Id))
		}
	})

	t.Run("the refusal is indistinguishable from having no verification in progress", func(t *testing.T) {
		foreign := addForeignCodeUser(t, "binding_foreign_msg@test.com", codeSentTo, nil)
		_, foreignErr := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &foreign, Code: foreignCodeText})

		none := addVerifyCodeTestUser(t, "binding_no_code@test.com", codeNowOnPhone, models.VerificationCode{}, nil, nil)
		_, noneErr := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &none, Code: foreignCodeText})

		if status.Code(foreignErr) != status.Code(noneErr) || status.Convert(foreignErr).Message() != status.Convert(noneErr).Message() {
			t.Errorf("the answer tells the two cases apart: %v vs %v", foreignErr, noneErr)
		}
	})

	t.Run("the refusal spends no verification attempt and keeps the number", func(t *testing.T) {
		token := addForeignCodeUser(t, "binding_foreign_budget@test.com", codeSentTo, nil)

		for i := 0; i < s.Intervals.MaxVerificationAttempts+1; i++ {
			if _, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &token, Code: foreignCodeText}); err == nil {
				t.Fatalf("attempt %d: the foreign code was accepted", i+1)
			}
		}

		if _, found := phoneOf(t, token.Id); !found {
			t.Error("the number was removed by a code that could never confirm it")
		}
		user, _ := testUserDBService.GetUserByID(testInstanceID, token.Id)
		if user.Account.PhoneVerificationCode.Attempts != 0 {
			t.Errorf("a code that cannot verify anything spent %d attempts", user.Account.PhoneVerificationCode.Attempts)
		}
	})

	t.Run("a code stored before the binding existed confirms nothing", func(t *testing.T) {
		// Codes written by the previous build carry no number. They cannot be attributed to
		// any number, so they are refused and the participant asks for a new one.
		legacy := validCode(foreignCodeText)
		users, err := addTestUsers([]models.User{{
			Account: models.Account{
				Type: "email", AccountID: "binding_legacy@test.com",
				PhoneVerificationCode: legacy,
			},
			ContactInfos: []models.ContactInfo{
				{ID: primitive.NewObjectID(), Type: models.ContactTypeEmail, Email: "binding_legacy@test.com", ConfirmedAt: time.Now().Unix()},
				{ID: primitive.NewObjectID(), Type: models.ContactTypePhone, Phone: codeNowOnPhone},
			},
		}})
		if err != nil {
			t.Fatalf("failed to create test user: %s", err.Error())
		}
		token := api_types.TokenInfos{Id: users[0].ID.Hex(), InstanceId: testInstanceID}

		if _, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &token, Code: foreignCodeText}); status.Code(err) != codes.InvalidArgument {
			t.Errorf("expected InvalidArgument, got %v", err)
		}
		if phone, _ := phoneOf(t, token.Id); phone.ConfirmedAt != 0 {
			t.Errorf("an unbound code confirmed a number: %+v", phone)
		}
	})

	t.Run("a code sent to the number still on the account verifies it", func(t *testing.T) {
		token := addVerifyCodeTestUser(t, "binding_matching@test.com", codeNowOnPhone, validCode(foreignCodeText), nil, nil)

		if _, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &token, Code: foreignCodeText}); err != nil {
			t.Fatalf("unexpected error: %s", err.Error())
		}
		if phone, _ := phoneOf(t, token.Id); phone.ConfirmedAt == 0 {
			t.Errorf("the number the code was sent to was not verified: %+v", phone)
		}
		if !hasChannel(channelsOf(t, token.Id), models.ChannelWhatsApp) {
			t.Errorf("whatsapp was not enabled: %v", channelsOf(t, token.Id))
		}
	})
}

// Every endpoint that hands a code to Meta must store it together with the number it went to:
// that binding is what later lets the verification tell one number from another.
func TestEverySentCodeIsStoredWithItsDestinationNumber(t *testing.T) {
	// Numbers of their own: the account they are added to must not collide with the seeded
	// users of the cases above.
	const firstNumber = "+391230000811"
	const secondNumber = "+391230000812"

	accountID := "binding_send_sites@test.com"
	mock := &countingWhatsAppClient{}
	s := newRateLimitTestServer(mock)

	users, err := addTestUsers([]models.User{{
		Account: models.Account{
			Type: "email", AccountID: accountID,
			AccountConfirmedAt:        time.Now().Unix(),
			PhoneVerificationAttempts: []int64{},
		},
		ContactInfos: []models.ContactInfo{
			{ID: primitive.NewObjectID(), Type: models.ContactTypeEmail, Email: accountID, ConfirmedAt: time.Now().Unix()},
		},
	}})
	if err != nil {
		t.Fatalf("failed to create test user: %s", err.Error())
	}
	token := api_types.TokenInfos{Id: users[0].ID.Hex(), InstanceId: testInstanceID}

	assertBoundTo := func(t *testing.T, endpoint string, phone string) {
		t.Helper()
		user, err := testUserDBService.GetUserByID(testInstanceID, token.Id)
		if err != nil {
			t.Fatalf("unexpected error: %s", err.Error())
		}
		if user.Account.PhoneVerificationCode.Phone != phone {
			t.Errorf("%s stored a code bound to %q instead of %q", endpoint, user.Account.PhoneVerificationCode.Phone, phone)
		}
	}

	if _, err := s.AddPhoneNumber(context.Background(), &api.PhoneMsg{Token: &token, NewPhone: firstNumber}); err != nil {
		t.Fatalf("AddPhoneNumber: %s", err.Error())
	}
	assertBoundTo(t, "AddPhoneNumber", firstNumber)

	// Clear the cooldown the first send stamped, so the resend is about the binding only.
	if err := testUserDBService.SetContactVerificationSentAt(testInstanceID, token.Id, models.ContactTypePhone, firstNumber, 0); err != nil {
		t.Fatalf("unexpected error: %s", err.Error())
	}
	if _, err := s.ResendContactVerification(context.Background(), &api.ResendContactVerificationReq{
		Token: &token, Type: models.ContactTypePhone, Address: firstNumber,
	}); err != nil {
		t.Fatalf("ResendContactVerification: %s", err.Error())
	}
	assertBoundTo(t, "ResendContactVerification", firstNumber)

	if _, err := s.EditPhoneNumber(context.Background(), &api.PhoneMsg{Token: &token, NewPhone: secondNumber}); err != nil {
		t.Fatalf("EditPhoneNumber: %s", err.Error())
	}
	assertBoundTo(t, "EditPhoneNumber", secondNumber)

	if mock.count() != 3 {
		t.Errorf("expected three sends, got %d", mock.count())
	}
}
