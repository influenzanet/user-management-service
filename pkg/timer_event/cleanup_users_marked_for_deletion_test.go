package timer_event

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coneno/logger"
	api_types "github.com/influenzanet/go-utils/pkg/api_types"
	loggingAPI "github.com/influenzanet/logging-service/pkg/api"
	messageAPI "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	studyAPI "github.com/influenzanet/study-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/dbs/globaldb"
	"github.com/influenzanet/user-management-service/pkg/dbs/userdb"
	"github.com/influenzanet/user-management-service/pkg/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const testDBNamePrefix = "TEST_TIMER_"

var (
	testUserDB   *userdb.UserDBService
	testGlobalDB *globaldb.GlobalDBService
	testInstance = "timer" + strconv.FormatInt(time.Now().UnixNano(), 10)
)

func TestMain(m *testing.M) {
	// Like the other database tests of this repository, these need the USER_DB_* variables.
	if os.Getenv("USER_DB_CONNECTION_STR") == "" {
		logger.Error.Fatal("Couldn't read DB credentials.")
	}
	uri := fmt.Sprintf("mongodb%s://%s:%s@%s", os.Getenv("USER_DB_CONNECTION_PREFIX"),
		os.Getenv("USER_DB_USERNAME"), os.Getenv("USER_DB_PASSWORD"), os.Getenv("USER_DB_CONNECTION_STR"))
	cfg := models.DBConfig{URI: uri, Timeout: 30, IdleConnTimeout: 45, MaxPoolSize: 8, DBNamePrefix: testDBNamePrefix}
	testUserDB = userdb.NewUserDBService(cfg)
	testGlobalDB = globaldb.NewGlobalDBService(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, err := testGlobalDB.DBClient.Database(testDBNamePrefix+"global-infos").Collection("instances").
		InsertOne(ctx, bson.M{"instanceID": testInstance})
	cancel()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	code := m.Run()

	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = testUserDB.DBClient.Database(testDBNamePrefix + testInstance + "_users").Drop(ctx)
	_ = testGlobalDB.DBClient.Database(testDBNamePrefix + "global-infos").Drop(ctx)
	os.Exit(code)
}

// fakeClients stands in for messaging, study and logging; every method the job does not call is
// left nil.
type fakeMessaging struct {
	messageAPI.MessagingServiceApiClient
	mu       sync.Mutex
	failures int
	cancels  []*messageAPI.DeleteOutgoingWhatsAppForUserReq
}

func (f *fakeMessaging) DeleteOutgoingWhatsAppForUser(_ context.Context, in *messageAPI.DeleteOutgoingWhatsAppForUserReq, _ ...grpc.CallOption) (*messageAPI.DeleteOutgoingWhatsAppForUserResp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels = append(f.cancels, in)
	if f.failures > 0 {
		f.failures--
		return nil, status.Error(codes.Unavailable, "down")
	}
	return &messageAPI.DeleteOutgoingWhatsAppForUserResp{}, nil
}

func (f *fakeMessaging) QueueEmailTemplateForSending(context.Context, *messageAPI.SendEmailReq, ...grpc.CallOption) (*messageAPI.ServiceStatus, error) {
	return &messageAPI.ServiceStatus{}, nil
}

type fakeStudy struct{ studyAPI.StudyServiceApiClient }

func (fakeStudy) ProfileDeleted(context.Context, *api_types.TokenInfos, ...grpc.CallOption) (*studyAPI.ServiceStatus, error) {
	return &studyAPI.ServiceStatus{}, nil
}

type fakeLogging struct {
	loggingAPI.LoggingServiceApiClient
}

func (fakeLogging) SaveLogEvent(context.Context, *loggingAPI.NewLogEvent, ...grpc.CallOption) (*api_types.ServiceStatus, error) {
	return &api_types.ServiceStatus{}, nil
}

func markedForDeletion(t *testing.T, accountID string, markedAt int64) string {
	t.Helper()
	id, err := testUserDB.AddUser(testInstance, models.User{
		Account:    models.Account{Type: "email", AccountID: accountID},
		Profiles:   []models.Profile{{ID: primitive.NewObjectID(), MainProfile: true}},
		Timestamps: models.Timestamps{MarkedForDeletion: markedAt},
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func newTimerService(messaging *fakeMessaging) *UserManagementTimerService {
	s := NewUserManagmentTimerService(60, testGlobalDB, testUserDB,
		&models.APIClients{MessagingService: messaging, StudyService: fakeStudy{}, LoggingService: fakeLogging{}},
		0, 0, 1, 1)
	s.whatsAppQueue.Backoff = []time.Duration{5 * time.Millisecond, 10 * time.Millisecond}
	return s
}

// An account deleted after inactivity leaves nothing queued or archived on WhatsApp.
func TestCleanupUsersMarkedForDeletionPurgesTheirWhatsApp(t *testing.T) {
	messaging := &fakeMessaging{}
	due := markedForDeletion(t, "timer_due@test.com", time.Now().Unix()-10)
	notDue := markedForDeletion(t, "timer_not_due@test.com", time.Now().Unix()+3600)

	newTimerService(messaging).CleanupUsersMarkedForDeletion()

	if _, err := testUserDB.GetUserByID(testInstance, due); err == nil {
		t.Fatal("setup: the account due for deletion was not deleted")
	}
	if len(messaging.cancels) != 1 {
		t.Fatalf("expected one purge, got %v", messaging.cancels)
	}
	req := messaging.cancels[0]
	if req.UserId != due || req.InstanceId != testInstance || !req.PurgeArchive || len(req.MessageTypes) != 0 {
		t.Errorf("unexpected purge request: %+v", req)
	}
	if req.UserId == notDue {
		t.Error("the account not yet due was purged")
	}
}

// Messaging down throughout: the deletion stands, an alert names the user.
func TestCleanupUsersMarkedForDeletionWithMessagingDown(t *testing.T) {
	messaging := &fakeMessaging{failures: 100}
	due := markedForDeletion(t, "timer_down@test.com", time.Now().Unix()-10)

	var buf bytes.Buffer
	original := logger.Error.Writer()
	logger.Error.SetOutput(&buf)
	newTimerService(messaging).CleanupUsersMarkedForDeletion()
	logger.Error.SetOutput(original)

	if _, err := testUserDB.GetUserByID(testInstance, due); err == nil {
		t.Error("the account was not deleted")
	}
	if len(messaging.cancels) != 3 {
		t.Errorf("expected 3 attempts, got %d", len(messaging.cancels))
	}
	if !strings.Contains(buf.String(), "WA_ALERT cancel_failed user="+due) {
		t.Errorf("missing alert: %q", buf.String())
	}
}
