package service

import (
	"context"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	api_types "github.com/influenzanet/go-utils/pkg/api_types"
	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/dbs/userdb"
	"github.com/influenzanet/user-management-service/pkg/models"
	loggingMock "github.com/influenzanet/user-management-service/test/mocks/logging_service"
	messageMock "github.com/influenzanet/user-management-service/test/mocks/messaging_service"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/bsontype"
)

// An empty channel list means no channel, and a missing one is a legacy account that reads as
// e-mail. So every account created from now on stores its channels explicitly: ["email"].

// storedChannelsOf reads preferredChannels as stored, without the model's decoding: the BSON type
// (0 when the field is missing) and the values.
func storedChannelsOf(t *testing.T, accountID string) (bsontype.Type, []string) {
	t.Helper()
	ctx, cancel := testUserDBService.GetContext()
	defer cancel()
	raw, err := testUserDBService.GetCollection(testInstanceID, userdb.UserCollection).
		FindOne(ctx, bson.M{"account.accountID": accountID}).DecodeBytes()
	if err != nil {
		t.Fatalf("the account was not stored: %v", err)
	}
	value := raw.Lookup("contactPreferences", "preferredChannels")
	if value.Type != bsontype.Array {
		return value.Type, nil
	}
	values, err := value.Array().Values()
	if err != nil {
		t.Fatal(err)
	}
	channels := []string{}
	for _, v := range values {
		channels = append(channels, v.StringValue())
	}
	return value.Type, channels
}

func requireStoredEmailOnly(t *testing.T, accountID string) {
	t.Helper()
	kind, channels := storedChannelsOf(t, accountID)
	if kind != bsontype.Array || len(channels) != 1 || channels[0] != models.ChannelEmail {
		t.Errorf("a new account must store [email], got BSON type %v with %v", kind, channels)
	}
}

func TestNewAccountsStoreEmailAsTheirChannel(t *testing.T) {
	t.Run("signup", func(t *testing.T) {
		s := newSignupTestServer(t)
		if _, err := s.SignupWithEmail(context.Background(), signupReq("new_account_signup@test.com", "")); err != nil {
			t.Fatal(err)
		}
		requireStoredEmailOnly(t, "new_account_signup@test.com")
	})

	t.Run("signup with a phone number", func(t *testing.T) {
		// The number is unverified, so it adds no channel.
		s := newSignupTestServer(t)
		if _, err := s.SignupWithEmail(context.Background(), signupReq("new_account_signup_phone@test.com", "+393230016001")); err != nil {
			t.Fatal(err)
		}
		requireStoredEmailOnly(t, "new_account_signup_phone@test.com")
	})

	t.Run("external identity provider", func(t *testing.T) {
		mockCtrl := gomock.NewController(t)
		logging := loggingMock.NewMockLoggingServiceApiClient(mockCtrl)
		logging.EXPECT().SaveLogEvent(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
		s := userManagementServer{
			userDBservice:   testUserDBService,
			globalDBService: testGlobalDBService,
			Intervals:       models.Intervals{TokenExpiryInterval: time.Second * 2},
			clients:         &models.APIClients{LoggingService: logging},
		}
		if _, err := s.LoginWithExternalIDP(context.Background(), &api.LoginWithExternalIDPMsg{
			InstanceId: testInstanceID, Email: "new_account_external@test.com", Role: "PARTICIPANT",
		}); err != nil {
			t.Fatal(err)
		}
		requireStoredEmailOnly(t, "new_account_external@test.com")
	})

	t.Run("created by an admin", func(t *testing.T) {
		mockCtrl := gomock.NewController(t)
		messaging := messageMock.NewMockMessagingServiceApiClient(mockCtrl)
		messaging.EXPECT().SendInstantEmail(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
		logging := loggingMock.NewMockLoggingServiceApiClient(mockCtrl)
		logging.EXPECT().SaveLogEvent(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
		s := userManagementServer{
			userDBservice:   testUserDBService,
			globalDBService: testGlobalDBService,
			Intervals:       models.Intervals{TokenExpiryInterval: time.Second * 2},
			clients:         &models.APIClients{MessagingService: messaging, LoggingService: logging},
		}
		if _, err := s.CreateUser(context.Background(), &api.CreateUserReq{
			Token: &api_types.TokenInfos{
				Id: "new_account_admin", InstanceId: testInstanceID, Payload: map[string]string{"roles": "PARTICIPANT,ADMIN"},
			},
			AccountId:       "new_account_admin_created@test.com",
			InitialPassword: "initPW543-d16",
			Roles:           []string{"PARTICIPANT"},
		}); err != nil {
			t.Fatal(err)
		}
		requireStoredEmailOnly(t, "new_account_admin_created@test.com")
	})
}
