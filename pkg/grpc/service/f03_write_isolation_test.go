package service

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	api_types "github.com/influenzanet/go-utils/pkg/api_types"
	"github.com/influenzanet/go-utils/pkg/constants"
	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/dbs/userdb"
	"github.com/influenzanet/user-management-service/pkg/models"
	"github.com/influenzanet/user-management-service/pkg/pwhash"
	loggingMock "github.com/influenzanet/user-management-service/test/mocks/logging_service"
	messageMock "github.com/influenzanet/user-management-service/test/mocks/messaging_service"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/event"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// f03FindBarrier stops an endpoint immediately after Mongo has answered its first user find.
// The reply (and therefore the stale snapshot) already exists at that point, while the callback
// still prevents the endpoint from decoding it and progressing to its write. Mutations use the
// package's ordinary, separate DB client, so they can complete while the endpoint is suspended.
type f03FindBarrier struct {
	mu        sync.Mutex
	armed     bool
	requestID int64
	reached   chan struct{}
	release   chan struct{}
}

func newF03FindBarrier() *f03FindBarrier {
	return &f03FindBarrier{
		reached: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (b *f03FindBarrier) monitor() *event.CommandMonitor {
	return &event.CommandMonitor{
		Started: func(_ context.Context, evt *event.CommandStartedEvent) {
			b.mu.Lock()
			defer b.mu.Unlock()
			if b.armed && b.requestID == 0 && evt.CommandName == "find" {
				b.requestID = evt.RequestID
			}
		},
		Succeeded: func(_ context.Context, evt *event.CommandSucceededEvent) {
			b.mu.Lock()
			matched := b.armed && b.requestID == evt.RequestID && evt.CommandName == "find"
			if matched {
				b.armed = false
			}
			b.mu.Unlock()
			if matched {
				close(b.reached)
				<-b.release
			}
		},
	}
}

func (b *f03FindBarrier) arm() {
	b.mu.Lock()
	b.armed = true
	b.mu.Unlock()
}

func (b *f03FindBarrier) wait(t *testing.T) {
	t.Helper()
	select {
	case <-b.reached:
	case <-time.After(5 * time.Second):
		close(b.release)
		t.Fatal("endpoint did not reach the post-find barrier")
	}
}

func newF03MonitoredUserDB(t *testing.T, barrier *f03FindBarrier) *userdb.UserDBService {
	t.Helper()

	timeout, err := strconv.Atoi(os.Getenv("DB_TIMEOUT"))
	if err != nil {
		t.Fatalf("DB_TIMEOUT: %v", err)
	}
	idleTimeout, err := strconv.Atoi(os.Getenv("DB_IDLE_CONN_TIMEOUT"))
	if err != nil {
		t.Fatalf("DB_IDLE_CONN_TIMEOUT: %v", err)
	}
	maxPoolSize, err := strconv.Atoi(os.Getenv("DB_MAX_POOL_SIZE"))
	if err != nil {
		t.Fatalf("DB_MAX_POOL_SIZE: %v", err)
	}
	uri := fmt.Sprintf(
		"mongodb%s://%s:%s@%s",
		os.Getenv("USER_DB_CONNECTION_PREFIX"),
		os.Getenv("USER_DB_USERNAME"),
		os.Getenv("USER_DB_PASSWORD"),
		os.Getenv("USER_DB_CONNECTION_STR"),
	)
	config := models.DBConfig{
		URI:             uri,
		Timeout:         timeout,
		IdleConnTimeout: idleTimeout,
		MaxPoolSize:     uint64(maxPoolSize),
		DBNamePrefix:    testDBNamePrefix,
	}

	// NewUserDBService establishes the private timeout/prefix fields. Replace only its public
	// client with an equivalent monitored client so the production constructor stays untouched.
	db := userdb.NewUserDBService(config)
	if err := db.DBClient.Disconnect(context.Background()); err != nil {
		t.Fatalf("disconnect initial DB client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().
		ApplyURI(uri).
		SetMaxConnIdleTime(time.Duration(idleTimeout)*time.Second).
		SetMaxPoolSize(uint64(maxPoolSize)).
		SetMonitor(barrier.monitor()))
	if err != nil {
		t.Fatalf("connect monitored DB client: %v", err)
	}
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })
	db.DBClient = client
	return db
}

type f03MutationCase struct {
	name  string
	setup func(*models.User)
	run   func(*testing.T, string)
}

func f03MutationCases() []f03MutationCase {
	newPhone := func(number string, confirmedAt int64) models.ContactInfo {
		return models.ContactInfo{
			ID:          primitive.NewObjectID(),
			Type:        models.ContactTypePhone,
			Phone:       number,
			ConfirmedAt: confirmedAt,
		}
	}
	code := func(value string, attempts int64) models.VerificationCode {
		now := time.Now().Unix()
		return models.VerificationCode{
			Code: value, Attempts: attempts, CreatedAt: now, ExpiresAt: now + 300,
		}
	}

	return []f03MutationCase{
		{
			name: "reserve_slot_and_replace_code",
			setup: func(user *models.User) {
				user.ContactInfos = append(user.ContactInfos, newPhone("+391230010001", 0))
				user.Account.PhoneVerificationCode = code("old-code", 0)
			},
			run: func(t *testing.T, userID string) {
				ok, err := testUserDBService.ReservePhoneVerificationSlot(
					testInstanceID, userID, allowedPhoneVerificationAttempts, phoneVerificationRateLimitWindow,
				)
				if err != nil || !ok {
					t.Fatalf("reserve phone verification slot: ok=%v err=%v", ok, err)
				}
				if err := testUserDBService.SetPhoneVerificationCode(testInstanceID, userID, code("new-code", 0)); err != nil {
					t.Fatalf("replace phone verification code: %v", err)
				}
			},
		},
		{
			name: "increment_code_attempts",
			setup: func(user *models.User) {
				user.ContactInfos = append(user.ContactInfos, newPhone("+391230010002", 0))
				user.Account.PhoneVerificationCode = code("attempt-code", 0)
			},
			run: func(t *testing.T, userID string) {
				if _, err := testUserDBService.IncrementVerificationCodeAttempts(testInstanceID, userID, 3); err != nil {
					t.Fatalf("increment phone verification attempts: %v", err)
				}
			},
		},
		{
			name:  "add_phone",
			setup: func(_ *models.User) {},
			run: func(t *testing.T, userID string) {
				ok, err := testUserDBService.AddPhoneContactInfoIfAbsent(
					testInstanceID, userID, newPhone("+391230010003", 0),
				)
				if err != nil || !ok {
					t.Fatalf("add phone: ok=%v err=%v", ok, err)
				}
			},
		},
		{
			name: "edit_phone",
			setup: func(user *models.User) {
				user.ContactInfos = append(user.ContactInfos, newPhone("+391230010004", time.Now().Unix()))
				user.ContactPreferences.PreferredChannels = []string{models.ChannelEmail, models.ChannelWhatsApp}
			},
			run: func(t *testing.T, userID string) {
				if err := testUserDBService.ReplacePhoneContactInfo(
					testInstanceID, userID, newPhone("+391230010104", 0),
				); err != nil {
					t.Fatalf("edit phone: %v", err)
				}
			},
		},
		{
			name: "delete_phone",
			setup: func(user *models.User) {
				user.ContactInfos = append(user.ContactInfos, newPhone("+391230010005", time.Now().Unix()))
				user.Account.PhoneVerificationCode = code("delete-code", 1)
				user.ContactPreferences.PreferredChannels = []string{models.ChannelEmail, models.ChannelWhatsApp}
			},
			run: func(t *testing.T, userID string) {
				if _, err := testUserDBService.DeletePhoneNumber(testInstanceID, userID); err != nil {
					t.Fatalf("delete phone: %v", err)
				}
			},
		},
		{
			name: "finalize_phone",
			setup: func(user *models.User) {
				user.ContactInfos = append(user.ContactInfos, newPhone("+391230010006", 0))
				user.Account.PhoneVerificationCode = code("finalize-code", 1)
				user.ContactPreferences.PreferredChannels = []string{models.ChannelEmail}
			},
			run: func(t *testing.T, userID string) {
				if _, err := testUserDBService.FinalizePhoneVerification(testInstanceID, userID); err != nil {
					t.Fatalf("finalize phone: %v", err)
				}
			},
		},
	}
}

func f03SeedUser(t *testing.T, accountID, hashedPassword string, mutation f03MutationCase) models.User {
	t.Helper()
	email := models.ContactInfo{
		ID:          primitive.NewObjectID(),
		Type:        models.ContactTypeEmail,
		Email:       accountID,
		ConfirmedAt: time.Now().Unix(),
	}
	user := models.User{
		Account: models.Account{
			Type:                      models.ACCOUNT_TYPE_EMAIL,
			AccountID:                 accountID,
			AccountConfirmedAt:        time.Now().Unix(),
			Password:                  hashedPassword,
			PreferredLanguage:         "en",
			FailedLoginAttempts:       []int64{},
			PasswordResetTriggers:     []int64{},
			PhoneVerificationAttempts: []int64{},
		},
		Roles: []string{"PARTICIPANT"},
		Profiles: []models.Profile{{
			ID: primitive.NewObjectID(), Alias: "main", MainProfile: true,
		}},
		ContactInfos: []models.ContactInfo{email},
		ContactPreferences: models.ContactPreferences{
			PreferredChannels: []string{models.ChannelEmail},
		},
		Timestamps: models.Timestamps{CreatedAt: time.Now().Unix()},
	}
	mutation.setup(&user)
	users, err := addTestUsers([]models.User{user})
	if err != nil {
		t.Fatalf("seed F-03 test user: %v", err)
	}
	return users[0]
}

func f03PhoneContacts(user models.User) []models.ContactInfo {
	phones := make([]models.ContactInfo, 0, 1)
	for _, contact := range user.ContactInfos {
		if contact.Type == models.ContactTypePhone {
			phones = append(phones, contact)
		}
	}
	return phones
}

func f03AssertProtectedDBState(t *testing.T, got, want models.User) {
	t.Helper()
	if !reflect.DeepEqual(got.Account.PhoneVerificationCode, want.Account.PhoneVerificationCode) {
		t.Errorf("phone verification code was overwritten: got %+v, want %+v", got.Account.PhoneVerificationCode, want.Account.PhoneVerificationCode)
	}
	if !reflect.DeepEqual(got.Account.PhoneVerificationAttempts, want.Account.PhoneVerificationAttempts) {
		t.Errorf("phone verification ledger was overwritten: got %v, want %v", got.Account.PhoneVerificationAttempts, want.Account.PhoneVerificationAttempts)
	}
	if gotPhones, wantPhones := f03PhoneContacts(got), f03PhoneContacts(want); !reflect.DeepEqual(gotPhones, wantPhones) {
		t.Errorf("phone contact infos were overwritten: got %+v, want %+v", gotPhones, wantPhones)
	}
	if !reflect.DeepEqual(got.ContactPreferences.PreferredChannels, want.ContactPreferences.PreferredChannels) {
		t.Errorf("preferred channels were overwritten: got %v, want %v", got.ContactPreferences.PreferredChannels, want.ContactPreferences.PreferredChannels)
	}
}

func f03AssertProtectedAPIState(t *testing.T, got *api.User, want models.User) {
	t.Helper()
	if got == nil {
		t.Fatal("endpoint returned a nil user")
	}
	wantAPI := want.ToAPI()
	gotPhones := make([]*api.ContactInfo, 0, 1)
	wantPhones := make([]*api.ContactInfo, 0, 1)
	for _, contact := range got.ContactInfos {
		if contact.Type == models.ContactTypePhone {
			gotPhones = append(gotPhones, contact)
		}
	}
	for _, contact := range wantAPI.ContactInfos {
		if contact.Type == models.ContactTypePhone {
			wantPhones = append(wantPhones, contact)
		}
	}
	if !reflect.DeepEqual(gotPhones, wantPhones) {
		t.Errorf("response contains stale phone contact infos: got %+v, want %+v", gotPhones, wantPhones)
	}
	if !reflect.DeepEqual(got.ContactPreferences.PreferredChannels, wantAPI.ContactPreferences.PreferredChannels) {
		t.Errorf("response contains stale preferred channels: got %v, want %v", got.ContactPreferences.PreferredChannels, wantAPI.ContactPreferences.PreferredChannels)
	}
}

func f03RunWhileSnapshotBlocked(
	t *testing.T,
	barrier *f03FindBarrier,
	call func() (*api.User, error),
	mutate func(),
) *api.User {
	t.Helper()
	result := make(chan struct {
		user *api.User
		err  error
	}, 1)
	barrier.arm()
	go func() {
		user, err := call()
		result <- struct {
			user *api.User
			err  error
		}{user: user, err: err}
	}()
	barrier.wait(t)
	func() {
		// A failed mutation must not leave the monitored Mongo callback blocked during cleanup.
		defer close(barrier.release)
		mutate()
	}()
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("endpoint failed: %v", got.err)
		}
		return got.user
	case <-time.After(10 * time.Second):
		t.Fatal("endpoint did not finish after releasing the find barrier")
		return nil
	}
}

func f03RunErrorWhileSnapshotBlocked(
	t *testing.T,
	barrier *f03FindBarrier,
	call func() error,
	mutate func(),
) error {
	t.Helper()
	result := make(chan error, 1)
	barrier.arm()
	go func() { result <- call() }()
	barrier.wait(t)
	func() {
		defer close(barrier.release)
		mutate()
	}()
	select {
	case err := <-result:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("endpoint did not finish after releasing the find barrier")
		return nil
	}
}

func TestF03LoginPreservesConcurrentWhatsAppWrites(t *testing.T) {
	const password = "SuperSecurePassword123!§$"
	hashedPassword, err := pwhash.HashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}

	for index, mutation := range f03MutationCases() {
		t.Run(mutation.name, func(t *testing.T) {
			accountID := fmt.Sprintf("f03-login-%02d@test.com", index)
			seeded := f03SeedUser(t, accountID, hashedPassword, mutation)
			barrier := newF03FindBarrier()
			monitoredDB := newF03MonitoredUserDB(t, barrier)
			mockCtrl := gomock.NewController(t)
			loggingClient := loggingMock.NewMockLoggingServiceApiClient(mockCtrl)
			loggingClient.EXPECT().SaveLogEvent(gomock.Any(), gomock.Any()).Return(nil, nil)
			server := userManagementServer{
				userDBservice:   monitoredDB,
				globalDBService: testGlobalDBService,
				clients:         &models.APIClients{LoggingService: loggingClient},
				Intervals:       models.Intervals{TokenExpiryInterval: time.Minute},
			}

			var want models.User
			responseUser := f03RunWhileSnapshotBlocked(t, barrier, func() (*api.User, error) {
				response, err := server.LoginWithEmail(context.Background(), &api.LoginWithEmailMsg{
					Email: accountID, Password: password, InstanceId: testInstanceID, AsParticipant: true,
				})
				if err != nil {
					return nil, err
				}
				return response.User, nil
			}, func() {
				mutation.run(t, seeded.ID.Hex())
				var readErr error
				want, readErr = testUserDBService.GetUserByID(testInstanceID, seeded.ID.Hex())
				if readErr != nil {
					t.Fatalf("read state after concurrent mutation: %v", readErr)
				}
			})

			got, err := testUserDBService.GetUserByID(testInstanceID, seeded.ID.Hex())
			if err != nil {
				t.Fatalf("read state after login: %v", err)
			}
			f03AssertProtectedDBState(t, got, want)
			f03AssertProtectedAPIState(t, responseUser, want)
		})
	}
}

func TestF03AddEmailPreservesConcurrentWhatsAppWrites(t *testing.T) {
	for index, mutation := range f03MutationCases() {
		t.Run(mutation.name, func(t *testing.T) {
			accountID := fmt.Sprintf("f03-add-email-%02d@test.com", index)
			newEmail := fmt.Sprintf("f03-added-%02d@test.com", index)
			seeded := f03SeedUser(t, accountID, "unused-password", mutation)
			barrier := newF03FindBarrier()
			monitoredDB := newF03MonitoredUserDB(t, barrier)
			mockCtrl := gomock.NewController(t)
			messagingClient := messageMock.NewMockMessagingServiceApiClient(mockCtrl)
			messagingClient.EXPECT().SendInstantEmail(gomock.Any(), gomock.Any()).Return(nil, nil)
			server := userManagementServer{
				userDBservice:   monitoredDB,
				globalDBService: testGlobalDBService,
				clients:         &models.APIClients{MessagingService: messagingClient},
			}
			token := api_types.TokenInfos{Id: seeded.ID.Hex(), InstanceId: testInstanceID}

			var want models.User
			responseUser := f03RunWhileSnapshotBlocked(t, barrier, func() (*api.User, error) {
				return server.AddEmail(context.Background(), &api.ContactInfoMsg{
					Token: &token,
					ContactInfo: &api.ContactInfo{
						Type: models.ContactTypeEmail, Address: &api.ContactInfo_Email{Email: newEmail},
					},
				})
			}, func() {
				mutation.run(t, seeded.ID.Hex())
				var readErr error
				want, readErr = testUserDBService.GetUserByID(testInstanceID, seeded.ID.Hex())
				if readErr != nil {
					t.Fatalf("read state after concurrent mutation: %v", readErr)
				}
			})

			got, err := testUserDBService.GetUserByID(testInstanceID, seeded.ID.Hex())
			if err != nil {
				t.Fatalf("read state after AddEmail: %v", err)
			}
			f03AssertProtectedDBState(t, got, want)
			f03AssertProtectedAPIState(t, responseUser, want)
			if _, found := got.FindContactInfoByTypeAndAddr(models.ContactTypeEmail, newEmail); !found {
				t.Errorf("AddEmail lost its intended non-phone mutation: %s was not stored", newEmail)
			}
		})
	}
}

func f03PreferenceMutationCases() []f03MutationCase {
	all := f03MutationCases()
	return []f03MutationCase{all[4], all[5]} // delete and finalize
}

func f03SeedSubscribedUser(t *testing.T, accountID string, mutation f03MutationCase) models.User {
	t.Helper()
	setup := mutation.setup
	mutation.setup = func(user *models.User) {
		setup(user)
		user.ContactPreferences.SubscribedToNewsletter = true
		user.ContactPreferences.SubscribedToWeekly = false
	}
	return f03SeedUser(t, accountID, "unused-password", mutation)
}

func f03TempToken(t *testing.T, userID, purpose string) string {
	t.Helper()
	token, err := testGlobalDBService.AddTempToken(models.TempToken{
		UserID: userID, InstanceID: testInstanceID, Purpose: purpose,
		Expiration: time.Now().Add(time.Minute).Unix(),
	})
	if err != nil {
		t.Fatalf("create %s token: %v", purpose, err)
	}
	return token
}

func TestF03UnsubscribePreservesConcurrentPhoneFinalizationAndDeletion(t *testing.T) {
	for index, mutation := range f03PreferenceMutationCases() {
		t.Run(mutation.name, func(t *testing.T) {
			seeded := f03SeedSubscribedUser(t, fmt.Sprintf("f03-unsubscribe-%02d@test.com", index), mutation)
			token := f03TempToken(t, seeded.ID.Hex(), constants.TOKEN_PURPOSE_UNSUBSCRIBE_NEWSLETTER)
			barrier := newF03FindBarrier()
			server := userManagementServer{
				userDBservice:   newF03MonitoredUserDB(t, barrier),
				globalDBService: testGlobalDBService,
			}

			var want models.User
			err := f03RunErrorWhileSnapshotBlocked(t, barrier, func() error {
				_, err := server.UseUnsubscribeToken(context.Background(), &api.TempToken{Token: token})
				return err
			}, func() {
				mutation.run(t, seeded.ID.Hex())
				var readErr error
				want, readErr = testUserDBService.GetUserByID(testInstanceID, seeded.ID.Hex())
				if readErr != nil {
					t.Fatalf("read state after concurrent mutation: %v", readErr)
				}
			})
			if err != nil {
				t.Fatalf("UseUnsubscribeToken failed: %v", err)
			}
			got, err := testUserDBService.GetUserByID(testInstanceID, seeded.ID.Hex())
			if err != nil {
				t.Fatalf("read state after unsubscribe: %v", err)
			}
			f03AssertProtectedDBState(t, got, want)
			if got.ContactPreferences.SubscribedToNewsletter {
				t.Error("unsubscribe did not persist its intended subscription change")
			}
		})
	}
}

func TestF03InvitationResetPreservesConcurrentPhoneFinalizationAndDeletion(t *testing.T) {
	for index, mutation := range f03PreferenceMutationCases() {
		t.Run(mutation.name, func(t *testing.T) {
			seeded := f03SeedSubscribedUser(t, fmt.Sprintf("f03-invitation-reset-%02d@test.com", index), mutation)
			token := f03TempToken(t, seeded.ID.Hex(), constants.TOKEN_PURPOSE_INVITATION)
			barrier := newF03FindBarrier()
			mockCtrl := gomock.NewController(t)
			messagingClient := messageMock.NewMockMessagingServiceApiClient(mockCtrl)
			loggingClient := loggingMock.NewMockLoggingServiceApiClient(mockCtrl)
			messagingClient.EXPECT().SendInstantEmail(gomock.Any(), gomock.Any()).Return(nil, nil)
			loggingClient.EXPECT().SaveLogEvent(gomock.Any(), gomock.Any()).Return(nil, nil)
			server := userManagementServer{
				userDBservice:   newF03MonitoredUserDB(t, barrier),
				globalDBService: testGlobalDBService,
				clients: &models.APIClients{
					MessagingService: messagingClient, LoggingService: loggingClient,
				},
			}

			var want models.User
			err := f03RunErrorWhileSnapshotBlocked(t, barrier, func() error {
				_, err := server.ResetPassword(context.Background(), &api.ResetPasswordMsg{
					Token: token, NewPassword: "AnotherSecurePassword123!",
				})
				return err
			}, func() {
				mutation.run(t, seeded.ID.Hex())
				var readErr error
				want, readErr = testUserDBService.GetUserByID(testInstanceID, seeded.ID.Hex())
				if readErr != nil {
					t.Fatalf("read state after concurrent mutation: %v", readErr)
				}
			})
			if err != nil {
				t.Fatalf("ResetPassword invitation failed: %v", err)
			}
			got, err := testUserDBService.GetUserByID(testInstanceID, seeded.ID.Hex())
			if err != nil {
				t.Fatalf("read state after invitation reset: %v", err)
			}
			f03AssertProtectedDBState(t, got, want)
			if !got.ContactPreferences.SubscribedToNewsletter || !got.ContactPreferences.SubscribedToWeekly {
				t.Errorf("invitation reset did not enable its intended subscriptions: %+v", got.ContactPreferences)
			}
		})
	}
}

func TestF03ExplicitPreferencesRejectConcurrentPhoneDeletionOrReplacement(t *testing.T) {
	all := f03MutationCases()
	mutations := []f03MutationCase{all[3], all[4]} // replace and delete
	for index, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			setup := mutation.setup
			mutation.setup = func(user *models.User) {
				setup(user)
				user.ContactPreferences.SubscribedToNewsletter = false
				user.ContactPreferences.SubscribedToWeekly = false
			}
			seeded := f03SeedUser(t, fmt.Sprintf("f03-explicit-prefs-%02d@test.com", index), "unused-password", mutation)
			barrier := newF03FindBarrier()
			server := userManagementServer{
				userDBservice:   newF03MonitoredUserDB(t, barrier),
				globalDBService: testGlobalDBService,
			}
			token := api_types.TokenInfos{Id: seeded.ID.Hex(), InstanceId: testInstanceID}

			var want models.User
			err := f03RunErrorWhileSnapshotBlocked(t, barrier, func() error {
				_, err := server.UpdateContactPreferences(context.Background(), &api.ContactPreferencesMsg{
					Token: &token,
					ContactPreferences: &api.ContactPreferences{
						SubscribedToNewsletter: true,
						SubscribedToWeekly:     true,
						PreferredChannels:      []string{models.ChannelEmail, models.ChannelWhatsApp},
					},
				})
				return err
			}, func() {
				mutation.run(t, seeded.ID.Hex())
				var readErr error
				want, readErr = testUserDBService.GetUserByID(testInstanceID, seeded.ID.Hex())
				if readErr != nil {
					t.Fatalf("read state after concurrent mutation: %v", readErr)
				}
			})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("expected InvalidArgument after concurrent phone change, got %v", err)
			}
			got, err := testUserDBService.GetUserByID(testInstanceID, seeded.ID.Hex())
			if err != nil {
				t.Fatalf("read state after rejected preferences update: %v", err)
			}
			if !reflect.DeepEqual(got.ContactPreferences, want.ContactPreferences) {
				t.Errorf("rejected preferences update committed partial fields: got %+v, want %+v", got.ContactPreferences, want.ContactPreferences)
			}
			f03AssertProtectedDBState(t, got, want)
		})
	}
}
