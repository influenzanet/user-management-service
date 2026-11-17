package service

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coneno/logger"
	"github.com/golang/mock/gomock"
	api_types "github.com/influenzanet/go-utils/pkg/api_types"
	"github.com/influenzanet/go-utils/pkg/constants"
	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/dbs/userdb"
	"github.com/influenzanet/user-management-service/pkg/models"
	"github.com/influenzanet/user-management-service/pkg/pwhash"
	messageMock "github.com/influenzanet/user-management-service/test/mocks/messaging_service"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const limiterTestPassword = "SuperSecurePassword123!§$"

func limiterTestServer(t *testing.T) *userManagementServer {
	mockCtrl := gomock.NewController(t)
	messaging := messageMock.NewMockMessagingServiceApiClient(mockCtrl)
	messaging.EXPECT().SendInstantEmail(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	return &userManagementServer{
		userDBservice:   testUserDBService,
		globalDBService: testGlobalDBService,
		instanceIDs:     []string{testInstanceID},
		Intervals: models.Intervals{
			TokenExpiryInterval:      time.Minute,
			VerificationCodeLifetime: 60,
		},
		clients: &models.APIClients{
			MessagingService: messaging,
			LoggingService:   &auditRecorder{},
		},
	}
}

// addLimiterTestUser stores a participant who can log in with limiterTestPassword.
func addLimiterTestUser(t *testing.T) models.User {
	t.Helper()
	hashed, err := pwhash.HashPassword(limiterTestPassword)
	if err != nil {
		t.Fatal(err)
	}
	user := models.User{
		Account: models.Account{
			Type:               "email",
			AccountID:          primitive.NewObjectID().Hex() + "@limiter.test",
			AccountConfirmedAt: time.Now().Unix(),
			Password:           hashed,
			PreferredLanguage:  "en",
		},
		Roles:    []string{constants.USER_ROLE_PARTICIPANT},
		Profiles: []models.Profile{{ID: primitive.NewObjectID(), MainProfile: true}},
	}
	users, err := addTestUsers([]models.User{user})
	if err != nil {
		t.Fatal(err)
	}
	return users[0]
}

func storedFailedLogins(t *testing.T, userID string) []interface{} {
	t.Helper()
	return storedCounterValues(t, userID, "failedLoginAttempts")
}

func storedCounterValues(t *testing.T, userID, field string) []interface{} {
	t.Helper()
	account, _ := rawUser(t, userID)["account"].(bson.M)
	values, ok := account[field].(bson.A)
	if !ok {
		t.Fatalf("account.%s is stored as %T, not as an array", field, account[field])
	}
	return values
}

// setAccountField writes account.<field> as it is; nil is stored as null.
func setAccountField(t *testing.T, userID, field string, value interface{}) {
	t.Helper()
	id, _ := primitive.ObjectIDFromHex(userID)
	if _, err := usersCollection().UpdateOne(context.Background(), bson.M{"_id": id},
		bson.M{"$set": bson.M{"account." + field: value}}); err != nil {
		t.Fatal(err)
	}
}

func loginReq(user models.User, password string) *api.LoginWithEmailMsg {
	return &api.LoginWithEmailMsg{
		Email:         user.Account.AccountID,
		Password:      password,
		InstanceId:    testInstanceID,
		AsParticipant: true,
	}
}

// Each counter write drops entries older than the retention, so the retention must cover the
// window the endpoints count over, or the limiters would undercount.
func TestRateLimitCounterRetentionCoversTheWindows(t *testing.T) {
	if userdb.FailedLoginAttemptRetention < loginFailedAttemptWindow {
		t.Errorf("failed logins are kept %d s, but counted over %d s", userdb.FailedLoginAttemptRetention, loginFailedAttemptWindow)
	}
	if userdb.PasswordResetTriggerRetention < passwordResetAttemptWindow {
		t.Errorf("reset requests are kept %d s, but counted over %d s", userdb.PasswordResetTriggerRetention, passwordResetAttemptWindow)
	}
}

// A successful login running at the same time as wrong-password attempts on the same account
// must not erase them: every wrong password is still counted afterwards.
func TestSuccessfulLoginKeepsConcurrentFailedAttempts(t *testing.T) {
	s := limiterTestServer(t)
	user := addLimiterTestUser(t)
	id := user.ID.Hex()

	// Few enough wrong passwords per round that the account never gets blocked.
	const rounds, wrong, right = 6, 4, 4
	for round := 0; round < rounds; round++ {
		setAccountField(t, id, "failedLoginAttempts", bson.A{})
		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := make(chan string, wrong+right)
		for i := 0; i < wrong+right; i++ {
			password := limiterTestPassword
			if i < wrong {
				password += "-wrong"
			}
			wg.Add(1)
			go func(password string) {
				defer wg.Done()
				<-start
				_, err := s.LoginWithEmail(context.Background(), loginReq(user, password))
				if password == limiterTestPassword && err != nil {
					errs <- "a correct login failed: " + err.Error()
				}
				if password != limiterTestPassword && status.Code(err) != codes.InvalidArgument {
					errs <- "a wrong password was not refused"
				}
			}(password)
		}
		close(start)
		wg.Wait()
		close(errs)
		for e := range errs {
			t.Fatal(e)
		}
		if got := len(storedFailedLogins(t, id)); got != wrong {
			t.Fatalf("round %d: %d wrong passwords, but %d failed attempts stored", round, wrong, got)
		}
	}
}

// Accounts created by an admin are protected by the login and reset limiters like any other.
func TestAdminCreatedAccountRecordsFailedLoginsAndResets(t *testing.T) {
	s := limiterTestServer(t)
	created, err := s.CreateUser(context.Background(), &api.CreateUserReq{
		Token: &api_types.TokenInfos{
			Id:         "admin",
			InstanceId: testInstanceID,
			Payload:    map[string]string{"roles": constants.USER_ROLE_ADMIN},
		},
		AccountId:          primitive.NewObjectID().Hex() + "@admin-created.test",
		InitialPassword:    limiterTestPassword,
		AccountConfirmedAt: time.Now().Unix(),
		Roles:              []string{constants.USER_ROLE_RESEARCHER},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.LoginWithEmail(context.Background(), &api.LoginWithEmailMsg{
		Email: created.Account.AccountId, Password: "wrong-" + limiterTestPassword, InstanceId: testInstanceID,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("wrong password not refused: %v", err)
	}
	if got := storedFailedLogins(t, created.Id); len(got) != 1 {
		t.Errorf("expected the failed login to be recorded, got %v", got)
	}

	if _, err := s.InitiatePasswordReset(context.Background(), &api.InitiateResetPasswordMsg{
		InstanceId: testInstanceID, AccountId: created.Account.AccountId,
	}); err != nil {
		t.Fatal(err)
	}
	if got := storedCounterValues(t, created.Id, "passwordResetTriggers"); len(got) != 1 {
		t.Errorf("expected the reset request to be recorded, got %v", got)
	}
}

// An account stored with null counters (created before they were initialised, never logged in
// since) still has its failed logins and reset requests counted.
func TestNullCountersStillRecordAttempts(t *testing.T) {
	s := limiterTestServer(t)
	user := addLimiterTestUser(t)
	id := user.ID.Hex()
	setAccountField(t, id, "failedLoginAttempts", nil)
	setAccountField(t, id, "passwordResetTriggers", nil)

	if _, err := s.LoginWithEmail(context.Background(), loginReq(user, "wrong")); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("wrong password not refused: %v", err)
	}
	if got := storedFailedLogins(t, id); len(got) != 1 {
		t.Errorf("expected the failed login to be recorded, got %v", got)
	}
	if _, err := s.InitiatePasswordReset(context.Background(), &api.InitiateResetPasswordMsg{
		InstanceId: testInstanceID, AccountId: user.Account.AccountID,
	}); err != nil {
		t.Fatal(err)
	}
	if got := storedCounterValues(t, id, "passwordResetTriggers"); len(got) != 1 {
		t.Errorf("expected the reset request to be recorded, got %v", got)
	}
}

// A stored password that cannot be read as a hash (an account created through an external
// identity provider has none) is a wrong password: refused and counted.
func TestSendVerificationCodeWithUnreadablePasswordHash(t *testing.T) {
	s := limiterTestServer(t)
	user := addLimiterTestUser(t)
	id := user.ID.Hex()
	setAccountField(t, id, "password", "")

	_, err := s.SendVerificationCode(context.Background(), &api.SendVerificationCodeReq{
		InstanceId: testInstanceID, Email: user.Account.AccountID, Password: limiterTestPassword,
	})
	if ok, msg := shouldHaveGrpcErrorStatus(err, "invalid username and/or password"); !ok {
		t.Fatal(msg)
	}
	if got := storedFailedLogins(t, id); len(got) != 1 {
		t.Errorf("expected the failed attempt to be recorded, got %v", got)
	}
}

// When the failed attempt cannot be stored, the wrong password is still refused and the error
// is logged.
func TestSendVerificationCodeLogsAFailedAttemptNotStored(t *testing.T) {
	s := limiterTestServer(t)
	user := addLimiterTestUser(t)
	// A validator that keeps this account's counter empty makes the append fail.
	refuseCounterWrites(t, user.ID)

	var buf bytes.Buffer
	original := logger.Error.Writer()
	logger.Error.SetOutput(&buf)
	defer logger.Error.SetOutput(original)

	_, err := s.SendVerificationCode(context.Background(), &api.SendVerificationCodeReq{
		InstanceId: testInstanceID, Email: user.Account.AccountID, Password: "wrong",
	})
	if ok, msg := shouldHaveGrpcErrorStatus(err, "invalid username and/or password"); !ok {
		t.Fatal(msg)
	}
	if !strings.Contains(buf.String(), "DB ERROR") {
		t.Errorf("the failed write was not logged: %q", buf.String())
	}
}

// refuseCounterWrites makes the users collection reject any write that leaves a failed login on
// this account, until the test ends.
func refuseCounterWrites(t *testing.T, userID primitive.ObjectID) {
	t.Helper()
	collMod := func(validator bson.M) error {
		return usersCollection().Database().RunCommand(context.Background(), bson.D{
			{Key: "collMod", Value: userdb.UserCollection},
			{Key: "validator", Value: validator},
			{Key: "validationLevel", Value: "strict"},
			{Key: "validationAction", Value: "error"},
		}).Err()
	}
	if err := collMod(bson.M{"$or": bson.A{
		bson.M{"_id": bson.M{"$ne": userID}},
		bson.M{"account.failedLoginAttempts": bson.M{"$size": 0}},
	}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := collMod(bson.M{}); err != nil {
			t.Errorf("cannot remove the validator: %v", err)
		}
	})
}
