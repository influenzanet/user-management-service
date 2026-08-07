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

// Changing the number leaves the account with a phone that has to be verified again, so the
// whatsapp channel cannot stay switched on: it would describe a delivery the platform cannot
// make. Deleting the number already revokes it — changing it is the same loss of a verified
// destination, and until now it kept the channel.

func TestEditPhoneNumberRevokesTheWhatsAppChannel(t *testing.T) {
	newEditChannelUser := func(t *testing.T, accountID string, phone string, channels []string) *api_types.TokenInfos {
		t.Helper()
		users, err := addTestUsers([]models.User{
			{
				Account: models.Account{
					Type:                      "email",
					AccountID:                 accountID,
					AccountConfirmedAt:        time.Now().Unix(),
					PhoneVerificationAttempts: []int64{},
				},
				ContactPreferences: models.ContactPreferences{PreferredChannels: channels},
				ContactInfos: []models.ContactInfo{
					{ID: primitive.NewObjectID(), Type: models.ContactTypeEmail, Email: accountID, ConfirmedAt: time.Now().Unix()},
					{ID: primitive.NewObjectID(), Type: models.ContactTypePhone, Phone: phone, ConfirmedAt: time.Now().Unix()},
				},
			},
		})
		if err != nil {
			t.Fatalf("failed to create test user: %s", err.Error())
		}
		return &api_types.TokenInfos{Id: users[0].ID.Hex(), InstanceId: testInstanceID}
	}

	t.Run("moving to a new number switches the channel off", func(t *testing.T) {
		mock := &countingWhatsAppClient{}
		s := newRateLimitTestServer(mock)
		token := newEditChannelUser(t, "edit_revokes_channel@test.com", "+391230000801",
			[]string{models.ChannelEmail, models.ChannelWhatsApp})

		if _, err := s.EditPhoneNumber(context.Background(), &api.PhoneMsg{Token: token, NewPhone: "+391230000802"}); err != nil {
			t.Fatalf("unexpected error: %s", err.Error())
		}

		user, err := testUserDBService.GetUserByID(testInstanceID, token.Id)
		if err != nil {
			t.Fatalf("unexpected error: %s", err.Error())
		}
		phone, found := user.FindContactInfoByTypeAndAddr(models.ContactTypePhone, "+391230000802")
		if !found || phone.ConfirmedAt != 0 {
			t.Fatalf("the new number should be there and unverified: %+v", phone)
		}
		channels := user.ContactPreferences.PreferredChannels
		for _, c := range channels {
			if c == models.ChannelWhatsApp {
				t.Errorf("whatsapp stayed enabled for a number that is no longer verified: %v", channels)
			}
		}
		hasEmail := false
		for _, c := range channels {
			if c == models.ChannelEmail {
				hasEmail = true
			}
		}
		if !hasEmail {
			t.Errorf("email delivery must survive the change: %v", channels)
		}
	})

	t.Run("resending on the same unverified number leaves the channels alone", func(t *testing.T) {
		mock := &countingWhatsAppClient{}
		s := newRateLimitTestServer(mock)
		token := newEditChannelUser(t, "edit_same_number@test.com", "+391230000803", []string{models.ChannelEmail})

		// The number is verified in the fixture, so ask for the same one: the endpoint treats
		// it as already verified and refuses, without disturbing the preferences.
		_, err := s.EditPhoneNumber(context.Background(), &api.PhoneMsg{Token: token, NewPhone: "+391230000803"})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("expected InvalidArgument for an already verified number, got %v", err)
		}

		user, _ := testUserDBService.GetUserByID(testInstanceID, token.Id)
		if len(user.ContactPreferences.PreferredChannels) != 1 {
			t.Errorf("a refused change must not touch the channels: %v", user.ContactPreferences.PreferredChannels)
		}
	})
}
