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

// Functional coverage of the phone verification endpoint, which had none: what a correct, a
// wrong, an expired and an exhausted code do to the user document and to the response.
//
// The channel assertions are the ones the client's TC-024 checks: verifying a phone must enable
// whatsapp next to email, and must not disturb the channels the user had already chosen.

func addVerifyCodeTestUser(t *testing.T, accountID string, phone string, code models.VerificationCode, channels []string, ledger []int64) api_types.TokenInfos {
	t.Helper()

	contactInfos := []models.ContactInfo{
		{ID: primitive.NewObjectID(), Type: models.ContactTypeEmail, Email: accountID, ConfirmedAt: time.Now().Unix()},
	}
	if phone != "" {
		contactInfos = append(contactInfos, models.ContactInfo{
			ID: primitive.NewObjectID(), Type: models.ContactTypePhone, Phone: phone,
		})
	}

	users, err := addTestUsers([]models.User{
		{
			Account: models.Account{
				Type:                      "email",
				AccountID:                 accountID,
				PhoneVerificationCode:     code,
				PhoneVerificationAttempts: ledger,
			},
			ContactPreferences: models.ContactPreferences{PreferredChannels: channels},
			ContactInfos:       contactInfos,
		},
	})
	if err != nil {
		t.Fatalf("failed to create test user: %s", err.Error())
	}
	return api_types.TokenInfos{Id: users[0].ID.Hex(), InstanceId: testInstanceID}
}

func validCode(code string) models.VerificationCode {
	return models.VerificationCode{
		Code:      code,
		Attempts:  0,
		CreatedAt: time.Now().Unix(),
		ExpiresAt: time.Now().Unix() + 60,
	}
}

func phoneOf(t *testing.T, userID string) (models.ContactInfo, bool) {
	t.Helper()
	user, err := testUserDBService.GetUserByID(testInstanceID, userID)
	if err != nil {
		t.Fatalf("unexpected error: %s", err.Error())
	}
	for _, ci := range user.ContactInfos {
		if ci.Type == models.ContactTypePhone {
			return ci, true
		}
	}
	return models.ContactInfo{}, false
}

func channelsOf(t *testing.T, userID string) []string {
	t.Helper()
	user, err := testUserDBService.GetUserByID(testInstanceID, userID)
	if err != nil {
		t.Fatalf("unexpected error: %s", err.Error())
	}
	return user.ContactPreferences.PreferredChannels
}

func hasChannel(channels []string, channel string) bool {
	for _, c := range channels {
		if c == channel {
			return true
		}
	}
	return false
}

func TestVerifyWhatsAppCode(t *testing.T) {
	s := newRateLimitTestServer(&countingWhatsAppClient{})
	s.Intervals.MaxVerificationAttempts = 3

	t.Run("rejects a request without a code", func(t *testing.T) {
		token := addVerifyCodeTestUser(t, "verify_no_code@test.com", "+391230000401", validCode("123456"), nil, nil)
		_, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &token})
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("expected InvalidArgument, got %v", err)
		}
	})

	t.Run("rejects a wrong code and counts the attempt", func(t *testing.T) {
		token := addVerifyCodeTestUser(t, "verify_wrong@test.com", "+391230000402", validCode("123456"), nil, nil)

		_, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &token, Code: "000000"})
		if status.Code(err) != codes.PermissionDenied {
			t.Errorf("expected PermissionDenied, got %v", err)
		}

		if phone, _ := phoneOf(t, token.Id); phone.ConfirmedAt != 0 {
			t.Errorf("the phone must stay unverified after a wrong code: %+v", phone)
		}
		user, _ := testUserDBService.GetUserByID(testInstanceID, token.Id)
		if user.Account.PhoneVerificationCode.Attempts != 1 {
			t.Errorf("the wrong attempt was not counted: %d", user.Account.PhoneVerificationCode.Attempts)
		}
		if len(channelsOf(t, token.Id)) != 0 {
			t.Errorf("no channel may be enabled by a failed verification: %v", channelsOf(t, token.Id))
		}
	})

	t.Run("rejects an expired code", func(t *testing.T) {
		expired := validCode("123456")
		expired.ExpiresAt = time.Now().Unix() - 1
		token := addVerifyCodeTestUser(t, "verify_expired@test.com", "+391230000403", expired, nil, nil)

		_, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &token, Code: "123456"})
		if status.Code(err) != codes.PermissionDenied {
			t.Errorf("expected PermissionDenied, got %v", err)
		}
		if phone, _ := phoneOf(t, token.Id); phone.ConfirmedAt != 0 {
			t.Errorf("an expired code must not verify the phone: %+v", phone)
		}
	})

	t.Run("a correct code verifies the phone and enables whatsapp next to email", func(t *testing.T) {
		ledger := []int64{time.Now().Unix() - 30}
		token := addVerifyCodeTestUser(t, "verify_ok@test.com", "+391230000404", validCode("123456"), nil, ledger)

		resp, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &token, Code: "123456"})
		if err != nil {
			t.Fatalf("unexpected error: %s", err.Error())
		}
		if resp == nil {
			t.Fatal("the endpoint must return the updated user")
		}

		phone, found := phoneOf(t, token.Id)
		if !found || phone.ConfirmedAt == 0 {
			t.Errorf("the phone was not verified: %+v", phone)
		}
		channels := channelsOf(t, token.Id)
		if !hasChannel(channels, models.ChannelWhatsApp) || !hasChannel(channels, models.ChannelEmail) {
			t.Errorf("both whatsapp and email must be enabled, got %v", channels)
		}
		user, _ := testUserDBService.GetUserByID(testInstanceID, token.Id)
		if user.Account.PhoneVerificationCode.Code != "" {
			t.Errorf("the spent code was not cleared: %+v", user.Account.PhoneVerificationCode)
		}
		if len(user.Account.PhoneVerificationAttempts) != len(ledger) {
			t.Errorf("the attempts ledger was rewritten: %v instead of %v", user.Account.PhoneVerificationAttempts, ledger)
		}
	})

	t.Run("a correct code keeps the channels the user already had", func(t *testing.T) {
		token := addVerifyCodeTestUser(t, "verify_keep_channels@test.com", "+391230000405", validCode("123456"), []string{models.ChannelEmail}, nil)

		if _, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &token, Code: "123456"}); err != nil {
			t.Fatalf("unexpected error: %s", err.Error())
		}

		channels := channelsOf(t, token.Id)
		if len(channels) != 2 || !hasChannel(channels, models.ChannelEmail) || !hasChannel(channels, models.ChannelWhatsApp) {
			t.Errorf("expected exactly email and whatsapp, got %v", channels)
		}
	})

	t.Run("too many attempts removes the phone together with its channel", func(t *testing.T) {
		exhausted := validCode("123456")
		exhausted.Attempts = 3 // at the cap: the next attempt takes the punitive branch
		token := addVerifyCodeTestUser(t, "verify_exhausted@test.com", "+391230000406", exhausted,
			[]string{models.ChannelEmail, models.ChannelWhatsApp}, nil)

		_, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &token, Code: "123456"})
		if status.Code(err) != codes.PermissionDenied {
			t.Errorf("expected PermissionDenied, got %v", err)
		}

		if phone, found := phoneOf(t, token.Id); found {
			t.Errorf("the phone should have been removed: %+v", phone)
		}
		channels := channelsOf(t, token.Id)
		if hasChannel(channels, models.ChannelWhatsApp) {
			t.Errorf("the whatsapp channel must go with the phone it depends on, got %v", channels)
		}
		if !hasChannel(channels, models.ChannelEmail) {
			t.Errorf("email delivery must survive the removal, got %v", channels)
		}
		user, _ := testUserDBService.GetUserByID(testInstanceID, token.Id)
		if user.Account.PhoneVerificationCode.Code != "" {
			t.Errorf("the code of a removed phone must be cleared: %+v", user.Account.PhoneVerificationCode)
		}
	})
}
