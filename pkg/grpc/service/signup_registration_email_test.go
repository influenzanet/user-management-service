package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	constants "github.com/influenzanet/go-utils/pkg/constants"
	messageAPI "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	loggingMock "github.com/influenzanet/user-management-service/test/mocks/logging_service"
	messageMock "github.com/influenzanet/user-management-service/test/mocks/messaging_service"
	"google.golang.org/grpc"
)

// Signup sends the registration e-mail in a goroutine of its own, while the request goes on to
// issue the tokens. The goroutine must not share any variable with the request: it used to
// write the request's err (item 8), a data race that could also hand the e-mail's error to the
// token checks that follow.
//
// The messaging call fails here so that the goroutine has an error to write, and the test waits
// for the call so that the goroutine runs while the test does. Under -race the shared variable
// is then always reported, whatever order the two goroutines happen to run in.
func TestSignupWithEmail_RegistrationEmailRunsOnItsOwn(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	t.Cleanup(mockCtrl.Finish)
	mockMessagingClient := messageMock.NewMockMessagingServiceApiClient(mockCtrl)
	mockLoggingClient := loggingMock.NewMockLoggingServiceApiClient(mockCtrl)
	mockLoggingClient.EXPECT().SaveLogEvent(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()

	sent := make(chan *messageAPI.SendEmailReq, 1)
	mockMessagingClient.EXPECT().SendInstantEmail(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, in *messageAPI.SendEmailReq, opts ...grpc.CallOption) (*messageAPI.ServiceStatus, error) {
			sent <- in
			return nil, errors.New("messaging service unreachable")
		}).Times(1)

	s := newSignupTestServer(t)
	s.clients.MessagingService = mockMessagingClient
	s.clients.LoggingService = mockLoggingClient

	// A fresh address on every run, so that -count=N does not trip over the account of the last.
	email := fmt.Sprintf("registration_email_%d@test.com", time.Now().UnixNano())
	resp, err := s.SignupWithEmail(context.Background(), signupReq(email, ""))

	// The e-mail is best effort: its failure must not fail the signup or its tokens.
	if err != nil {
		t.Fatalf("signup failed although only the registration e-mail did: %s", err.Error())
	}
	if resp.AccessToken == "" || resp.RefreshToken == "" {
		t.Fatalf("signup answered without tokens: access %q, refresh %q", resp.AccessToken, resp.RefreshToken)
	}

	var req *messageAPI.SendEmailReq
	select {
	case req = <-sent:
	case <-time.After(10 * time.Second):
		t.Fatal("the registration e-mail was never sent")
	}

	if req.InstanceId != testInstanceID {
		t.Errorf("e-mail sent for instance %q, want %q", req.InstanceId, testInstanceID)
	}
	if len(req.To) != 1 || req.To[0] != email {
		t.Errorf("e-mail sent to %v, want [%s]", req.To, email)
	}
	if req.MessageType != constants.EMAIL_TYPE_REGISTRATION {
		t.Errorf("e-mail of type %q, want %q", req.MessageType, constants.EMAIL_TYPE_REGISTRATION)
	}
	if req.PreferredLanguage != "en" {
		t.Errorf("e-mail in language %q, want the signup's %q", req.PreferredLanguage, "en")
	}

	// The link in the e-mail must verify this account's address.
	user, err := testUserDBService.GetUserByAccountID(testInstanceID, email)
	if err != nil {
		t.Fatalf("the new account cannot be read back: %s", err.Error())
	}
	token, err := testGlobalDBService.GetTempToken(req.ContentInfos["token"])
	if err != nil {
		t.Fatalf("the e-mail carries a token that does not exist (%q): %s", req.ContentInfos["token"], err.Error())
	}
	if token.UserID != user.ID.Hex() || token.InstanceID != testInstanceID {
		t.Errorf("the e-mail's token belongs to user %q of %q, want %q of %q", token.UserID, token.InstanceID, user.ID.Hex(), testInstanceID)
	}
	if token.Purpose != constants.TOKEN_PURPOSE_CONTACT_VERIFICATION || token.Info["email"] != email {
		t.Errorf("the e-mail's token is for %q on %q, want %q on %q", token.Purpose, token.Info["email"], constants.TOKEN_PURPOSE_CONTACT_VERIFICATION, email)
	}
}
