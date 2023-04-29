package service

import (
	"context"
	"fmt"
	"log"
	"time"

	constants "github.com/influenzanet/go-utils/pkg/constants"
	messageAPI "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	"github.com/influenzanet/user-management-service/pkg/models"
	"github.com/influenzanet/user-management-service/pkg/tokens"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *userManagementServer) generateAndSendVerificationCode(instanceID string, user models.User) error {
	vc, err := tokens.GenerateVerificationCode(6)
	if err != nil {
		log.Printf("unexpected error while generating verification code: %v", err)
		return status.Error(codes.Internal, "error while generating verification code")
	}

	user.Account.VerificationCode = models.VerificationCode{
		Code:      vc,
		Attempts:  0,
		CreatedAt: time.Now().Unix(),
		ExpiresAt: time.Now().Unix() + s.Intervals.VerificationCodeLifetime,
	}
	user, err = s.userDBservice.UpdateUser(instanceID, user)
	if err != nil {
		log.Printf("generateAndSendVerificationCode: unexpected error when saving user -> %v", err)
		return status.Error(codes.Internal, "user couldn't be updated")
	}

	// ---> Trigger message sending
	half := len(vc) / 2
	formattedCode := fmt.Sprintf("%s-%s", vc[:half], vc[half:])
	go s.sendVerificationEmail(instanceID, user.Account.AccountID, formattedCode, user.Account.PreferredLanguage)
	return nil
}

func (s *userManagementServer) sendVerificationEmail(instanceID string, accountID string, code string, preferredLang string) {
	if s.clients.MessagingService == nil {
		return
	}
	_, err := s.clients.MessagingService.SendInstantEmail(context.TODO(), &messageAPI.SendEmailReq{
		InstanceId:  instanceID,
		To:          []string{accountID},
		MessageType: constants.EMAIL_TYPE_AUTH_VERIFICATION_CODE,
		ContentInfos: map[string]string{
			"verificationCode": code,
		},
		PreferredLanguage: preferredLang,
	})
	if err != nil {
		log.Printf("SendVerificationCode: %s", err.Error())
	}
}

func (s *userManagementServer) isInstanceIDAllowed(instanceID string) bool {
	for _, id := range s.instanceIDs {
		if id == instanceID {
			return true
		}
	}
	return false
}
