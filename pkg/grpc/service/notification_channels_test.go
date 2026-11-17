package service

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	api_types "github.com/influenzanet/go-utils/pkg/api_types"
	api "github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestResolvePreferredChannels(t *testing.T) {
	withPhone := models.User{ContactInfos: []models.ContactInfo{
		{Type: models.ContactTypePhone, Phone: "+393230000601", ConfirmedAt: time.Now().Unix()},
	}}
	unverifiedPhone := models.User{ContactInfos: []models.ContactInfo{
		{Type: models.ContactTypePhone, Phone: "+393230000602"},
	}}
	noPhone := models.User{}

	accepted := []struct {
		name      string
		requested []string
		user      models.User
		want      []string
	}{
		{"whatsapp only with a verified phone", []string{models.ChannelWhatsApp}, withPhone, []string{models.ChannelWhatsApp}},
		{"email only", []string{models.ChannelEmail}, noPhone, []string{models.ChannelEmail}},
		{"both, in the requested order", []string{models.ChannelEmail, models.ChannelWhatsApp}, withPhone, []string{models.ChannelEmail, models.ChannelWhatsApp}},
		{"both, whatsapp first", []string{models.ChannelWhatsApp, models.ChannelEmail}, withPhone, []string{models.ChannelWhatsApp, models.ChannelEmail}},
		{"duplicates are collapsed", []string{models.ChannelEmail, models.ChannelWhatsApp, models.ChannelEmail, models.ChannelWhatsApp}, withPhone, []string{models.ChannelEmail, models.ChannelWhatsApp}},
		{"repeated email", []string{models.ChannelEmail, models.ChannelEmail}, noPhone, []string{models.ChannelEmail}},
		// No channel is a choice, stored as an empty list and never completed with e-mail.
		{"an empty list means no channel", []string{}, withPhone, []string{}},
		{"a nil list means no channel", nil, noPhone, []string{}},
	}
	for _, c := range accepted {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolvePreferredChannels(c.requested, c.user)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("expected %v, got %v", c.want, got)
			}
			// nil would be stored as null and read back as the legacy e-mail default.
			if got == nil {
				t.Error("the list to persist must never be nil")
			}
		})
	}

	refused := []struct {
		name      string
		requested []string
		user      models.User
	}{
		{"whatsapp without any phone", []string{models.ChannelWhatsApp}, noPhone},
		{"whatsapp with an unverified phone", []string{models.ChannelWhatsApp}, unverifiedPhone},
		{"email and whatsapp without a verified phone", []string{models.ChannelEmail, models.ChannelWhatsApp}, unverifiedPhone},
		{"unknown channel", []string{"telegram"}, withPhone},
		{"unknown channel next to a valid one", []string{models.ChannelEmail, "telegram"}, withPhone},
	}
	for _, c := range refused {
		t.Run("refuses "+c.name, func(t *testing.T) {
			got, err := resolvePreferredChannels(c.requested, c.user)
			if status.Code(err) != codes.InvalidArgument {
				t.Errorf("expected InvalidArgument, got %v (channels %v)", err, got)
			}
		})
	}
}

func TestUpdateContactPreferencesStoresExactlyTheChosenChannels(t *testing.T) {
	s := userManagementServer{
		userDBservice:   testUserDBService,
		globalDBService: testGlobalDBService,
		Intervals:       models.Intervals{TokenExpiryInterval: time.Second * 2},
	}

	seed := func(t *testing.T, accountID string, channels []string) *api_types.TokenInfos {
		t.Helper()
		users, err := addTestUsers([]models.User{{
			Account: models.Account{Type: "email", AccountID: accountID},
			ContactInfos: []models.ContactInfo{
				{ID: primitive.NewObjectID(), Type: models.ContactTypeEmail, Email: accountID, ConfirmedAt: time.Now().Unix()},
				{ID: primitive.NewObjectID(), Type: models.ContactTypePhone, Phone: "+393230000611", ConfirmedAt: time.Now().Unix()},
			},
			ContactPreferences: models.ContactPreferences{PreferredChannels: channels},
		}})
		if err != nil {
			t.Fatalf("failed to create test user: %s", err.Error())
		}
		return &api_types.TokenInfos{Id: users[0].ID.Hex(), InstanceId: testInstanceID}
	}
	update := func(token *api_types.TokenInfos, channels []string) (*api.User, error) {
		return s.UpdateContactPreferences(context.Background(), &api.ContactPreferencesMsg{
			Token:              token,
			ContactPreferences: &api.ContactPreferences{PreferredChannels: channels},
		})
	}
	stored := func(token *api_types.TokenInfos) []string {
		user, err := testUserDBService.GetUserByID(testInstanceID, token.Id)
		if err != nil {
			t.Fatalf("failed to read user: %v", err)
		}
		return user.ContactPreferences.PreferredChannels
	}

	t.Run("whatsapp only is persisted without email being added back", func(t *testing.T) {
		token := seed(t, "exact_whatsapp_only@test.com", []string{models.ChannelEmail, models.ChannelWhatsApp})
		resp, err := update(token, []string{models.ChannelWhatsApp})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := []string{models.ChannelWhatsApp}; !reflect.DeepEqual(resp.ContactPreferences.PreferredChannels, want) {
			t.Errorf("response: expected %v, got %v", want, resp.ContactPreferences.PreferredChannels)
		}
		if got, want := stored(token), []string{models.ChannelWhatsApp}; !reflect.DeepEqual(got, want) {
			t.Errorf("stored: expected %v, got %v", want, got)
		}
	})

	t.Run("unchecking email and checking it again round-trips", func(t *testing.T) {
		token := seed(t, "exact_roundtrip@test.com", []string{models.ChannelEmail})
		for _, want := range [][]string{
			{models.ChannelWhatsApp},
			{models.ChannelEmail, models.ChannelWhatsApp},
			{models.ChannelEmail},
		} {
			if _, err := update(token, want); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := stored(token); !reflect.DeepEqual(got, want) {
				t.Errorf("expected %v, got %v", want, got)
			}
		}
	})

	t.Run("an empty list is stored as no channel", func(t *testing.T) {
		for i, channels := range [][]string{nil, {}} {
			token := seed(t, fmt.Sprintf("exact_empty_stored_%d@test.com", i), []string{models.ChannelEmail, models.ChannelWhatsApp})
			resp, err := update(token, channels)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := resp.ContactPreferences.PreferredChannels; len(got) != 0 {
				t.Errorf("response: expected no channel, got %v", got)
			}
			if got := stored(token); got == nil || len(got) != 0 {
				t.Errorf("stored: expected an empty list, got %#v", got)
			}
			// And reading it back (as messaging does, over gRPC) still says no channel.
			user, err := s.GetUser(context.Background(), &api.UserReference{Token: token})
			if err != nil {
				t.Fatal(err)
			}
			if got := user.ContactPreferences.PreferredChannels; len(got) != 0 {
				t.Errorf("GetUser: expected no channel, got %v", got)
			}
		}
	})

	t.Run("after the phone is deleted no channel is left and whatsapp cannot be chosen", func(t *testing.T) {
		token := seed(t, "exact_after_delete@test.com", []string{models.ChannelWhatsApp})
		if _, err := testUserDBService.DeletePhoneNumber(testInstanceID, token.Id); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := stored(token); got == nil || len(got) != 0 {
			t.Errorf("expected no channel (no e-mail fallback), got %#v", got)
		}
		_, err := update(token, []string{models.ChannelWhatsApp})
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("expected InvalidArgument without a verified phone, got %v", err)
		}
	})
}

// Through the endpoint, a burst of preference updates racing with the removal of the phone
// never leaves the channel list missing (which would read as e-mail), nor with whatsapp and no
// verified phone. An empty list is a legitimate outcome.
func TestUpdateContactPreferencesUnderConcurrentPhoneRemoval(t *testing.T) {
	s := userManagementServer{
		userDBservice:   testUserDBService,
		globalDBService: testGlobalDBService,
		Intervals:       models.Intervals{TokenExpiryInterval: time.Second * 2},
	}
	requests := [][]string{
		{models.ChannelWhatsApp},
		{models.ChannelEmail},
		{models.ChannelEmail, models.ChannelWhatsApp},
	}

	for _, removal := range []string{"delete", "replace"} {
		t.Run(removal, func(t *testing.T) {
			for i := 0; i < 15; i++ {
				accountID := fmt.Sprintf("endpoint_conc_%s_%d@test.com", removal, i)
				phone := fmt.Sprintf("+3932300062%02d", i)
				users, err := addTestUsers([]models.User{{
					Account: models.Account{Type: "email", AccountID: accountID},
					ContactInfos: []models.ContactInfo{
						{ID: primitive.NewObjectID(), Type: models.ContactTypeEmail, Email: accountID, ConfirmedAt: time.Now().Unix()},
						{ID: primitive.NewObjectID(), Type: models.ContactTypePhone, Phone: phone, ConfirmedAt: time.Now().Unix()},
					},
					ContactPreferences: models.ContactPreferences{PreferredChannels: []string{models.ChannelWhatsApp}},
				}})
				if err != nil {
					t.Fatalf("failed to create test user: %s", err.Error())
				}
				token := &api_types.TokenInfos{Id: users[0].ID.Hex(), InstanceId: testInstanceID}

				start := make(chan struct{})
				var wg sync.WaitGroup
				for w := 0; w < 9; w++ {
					wg.Add(1)
					go func(w int) {
						defer wg.Done()
						<-start
						_, err := s.UpdateContactPreferences(context.Background(), &api.ContactPreferencesMsg{
							Token:              token,
							ContactPreferences: &api.ContactPreferences{PreferredChannels: requests[w%len(requests)]},
						})
						// A refusal is the correct answer once the phone is gone.
						if err != nil && status.Code(err) != codes.InvalidArgument {
							t.Errorf("unexpected error: %v", err)
						}
					}(w)
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					var err error
					if removal == "delete" {
						_, err = testUserDBService.DeletePhoneNumber(testInstanceID, token.Id)
					} else {
						err = testUserDBService.ReplacePhoneContactInfo(testInstanceID, token.Id, models.ContactInfo{
							ID: primitive.NewObjectID(), Type: models.ContactTypePhone, Phone: phone + "9",
						})
					}
					if err != nil {
						t.Errorf("unexpected error: %v", err)
					}
				}()
				close(start)
				wg.Wait()

				user, err := testUserDBService.GetUserByID(testInstanceID, token.Id)
				if err != nil {
					t.Fatalf("failed to read user: %v", err)
				}
				channels := user.ContactPreferences.PreferredChannels
				if channels == nil {
					t.Fatalf("iteration %d: the channel list is missing, which reads as e-mail", i)
				}
				for _, ch := range channels {
					if ch == models.ChannelWhatsApp {
						t.Fatalf("iteration %d: whatsapp stored while the phone is gone: %v", i, channels)
					}
				}
			}
		})
	}
}
