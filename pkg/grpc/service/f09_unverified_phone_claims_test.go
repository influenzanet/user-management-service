package service

import (
	"context"
	"testing"

	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/models"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// F-09: an unverified claim held a number for good. Somebody mistypes a digit at signup, or
// abandons the signup before using the code, and the number they never proved control of is
// closed to the participant it actually belongs to, with no way back short of a database edit.
//
// The rule is now that only a verified contact holds a number, and verifying one releases every
// unverified claim to it left on other accounts.

const (
	contestedPhone = "+391230001101"
	claimantCode   = "111111"
)

// storedPhoneCode reads back the code the service generated and sent, which the test needs in
// order to complete a verification it did not seed by hand.
func storedPhoneCode(t *testing.T, userID string) string {
	t.Helper()
	user, err := testUserDBService.GetUserByID(testInstanceID, userID)
	if err != nil {
		t.Fatalf("unexpected error: %s", err.Error())
	}
	return user.Account.PhoneVerificationCode.Code
}

func TestVerifyWhatsAppCodeReleasesUnverifiedClaims(t *testing.T) {
	s := newRateLimitTestServer(&countingWhatsAppClient{})
	s.Intervals.MaxVerificationAttempts = 3

	// The claimant typed the number at signup and never verified it.
	claimant := addVerifyCodeTestUser(t, "f09_claimant@test.com", contestedPhone, validCode(claimantCode), nil, nil)
	// The real owner registers the same number afterwards.
	owner := addRateLimitTestUser(t, "f09_owner@test.com", nil)

	t.Run("the real owner can register a number somebody else only claimed", func(t *testing.T) {
		if _, err := s.AddPhoneNumber(context.Background(), &api.PhoneMsg{Token: &owner, NewPhone: contestedPhone}); err != nil {
			t.Fatalf("an unverified claim still blocks the real owner: %s", err.Error())
		}
	})

	t.Run("verifying it releases the claim and the code bound to it", func(t *testing.T) {
		code := storedPhoneCode(t, owner.Id)
		if code == "" {
			t.Fatal("no code was stored for the owner")
		}
		if _, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &owner, Code: code}); err != nil {
			t.Fatalf("the owner could not verify the number: %s", err.Error())
		}

		phone, found := phoneOf(t, owner.Id)
		if !found || phone.ConfirmedAt <= 0 {
			t.Fatalf("the owner's number was not confirmed: %+v", phone)
		}

		if _, stillThere := phoneOf(t, claimant.Id); stillThere {
			t.Error("the stale claim survived a proven verification of the same number")
		}
		claimantUser, err := testUserDBService.GetUserByID(testInstanceID, claimant.Id)
		if err != nil {
			t.Fatalf("unexpected error: %s", err.Error())
		}
		if claimantUser.Account.PhoneVerificationCode.Code != "" {
			t.Errorf("the released claim kept a usable code: %+v", claimantUser.Account.PhoneVerificationCode)
		}
	})

	t.Run("the released claimant's code verifies nothing", func(t *testing.T) {
		_, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &claimant, Code: claimantCode})
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("expected InvalidArgument, got %v", err)
		}
		if _, stillThere := phoneOf(t, claimant.Id); stillThere {
			t.Error("a released claim came back")
		}
	})

	t.Run("a number verified by somebody else is still refused", func(t *testing.T) {
		latecomer := addRateLimitTestUser(t, "f09_latecomer@test.com", nil)
		_, err := s.AddPhoneNumber(context.Background(), &api.PhoneMsg{Token: &latecomer, NewPhone: contestedPhone})
		if ok, msg := shouldHaveGrpcErrorStatus(err, "phone number already taken"); !ok {
			t.Error(msg)
		}
	})
}

// TestVerifyWhatsAppCodeKeepsOtherContacts guards the release against reaching further than the
// one contact it is meant to remove.
func TestVerifyWhatsAppCodeKeepsOtherContacts(t *testing.T) {
	s := newRateLimitTestServer(&countingWhatsAppClient{})
	s.Intervals.MaxVerificationAttempts = 3

	const ownPhone = "+391230001102"
	const bystanderPhone = "+391230001103"

	bystander := addVerifyCodeTestUser(t, "f09_bystander@test.com", bystanderPhone, validCode("222222"), nil, nil)
	token := addVerifyCodeTestUser(t, "f09_verifier@test.com", ownPhone, validCode("333333"), nil, nil)

	if _, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &token, Code: "333333"}); err != nil {
		t.Fatalf("unexpected error: %s", err.Error())
	}

	phone, found := phoneOf(t, bystander.Id)
	if !found || phone.Phone != bystanderPhone {
		t.Errorf("an unrelated claim was released: %+v", phone)
	}
	user, err := testUserDBService.GetUserByID(testInstanceID, bystander.Id)
	if err != nil {
		t.Fatalf("unexpected error: %s", err.Error())
	}
	if user.Account.PhoneVerificationCode.Code != "222222" {
		t.Errorf("an unrelated code was reset: %+v", user.Account.PhoneVerificationCode)
	}

	// The verifier keeps every contact it had, e-mail included.
	verifier, err := testUserDBService.GetUserByID(testInstanceID, token.Id)
	if err != nil {
		t.Fatalf("unexpected error: %s", err.Error())
	}
	if _, ok := verifier.FindContactInfoByTypeAndAddr(models.ContactTypeEmail, "f09_verifier@test.com"); !ok {
		t.Errorf("the verification removed the e-mail contact: %+v", verifier.ContactInfos)
	}
}

// GAP1: the signup path. The rule change reaches it through IsPhoneNumberTaken, which delegates
// to the helper F-09 narrowed, but nothing above drives SignupWithEmail itself.
func TestSignupWithPhoneHeldUnverifiedByAnotherUser(t *testing.T) {
	const claimedAtSignup = "+391230001201"
	const verifiedAtSignup = "+391230001202"
	s := newSignupTestServer(t)

	t.Run("a number only claimed by somebody else no longer blocks registration", func(t *testing.T) {
		addVerifyCodeTestUser(t, "f09_signup_claimant@test.com", claimedAtSignup, validCode("700001"), nil, nil)

		if _, err := s.SignupWithEmail(context.Background(), signupReq("f09_signup_owner@test.com", claimedAtSignup)); err != nil {
			t.Fatalf("signup refused a number nobody had proved control of: %s", err.Error())
		}
		user, err := testUserDBService.GetUserByAccountID(testInstanceID, "f09_signup_owner@test.com")
		if err != nil {
			t.Fatalf("the account was not created: %s", err.Error())
		}
		if ci, found := user.FindContactInfoByTypeAndAddr(models.ContactTypePhone, claimedAtSignup); !found || ci.ConfirmedAt != 0 {
			t.Errorf("the number was not stored unverified: %+v", user.ContactInfos)
		}
	})

	t.Run("a number somebody has verified is still refused, and refused like a duplicate address", func(t *testing.T) {
		// The F-08 answer, not the old AlreadyExists one: the two findings compose here.
		owner := addVerifyCodeTestUser(t, "f09_signup_verified_owner@test.com", verifiedAtSignup, models.VerificationCode{}, nil, nil)
		if _, err := testUserDBService.FinalizePhoneVerification(testInstanceID, owner.Id, verifiedAtSignup); err != nil {
			t.Fatalf("failed to seed the verified owner: %s", err.Error())
		}

		_, err := s.SignupWithEmail(context.Background(), signupReq("f09_signup_blocked@test.com", verifiedAtSignup))
		if ok, msg := shouldHaveGrpcErrorStatus(err, "user creation failed"); !ok {
			t.Error(msg)
		}
		if status.Code(err) != codes.Internal {
			t.Errorf("expected Internal, got %v", status.Code(err))
		}
	})
}

// GAP2: the change-phone path reaches the same rule and the same release.
func TestEditPhoneNumberOverUnverifiedClaim(t *testing.T) {
	const contested = "+391230001211"
	const ownNumber = "+391230001212"
	s := newRateLimitTestServer(&countingWhatsAppClient{})
	s.Intervals.MaxVerificationAttempts = 3

	claimant := addVerifyCodeTestUser(t, "f09_edit_claimant@test.com", contested, validCode("800001"), nil, nil)
	mover := addVerifyCodeTestUser(t, "f09_edit_mover@test.com", ownNumber, models.VerificationCode{}, nil, nil)

	if _, err := s.EditPhoneNumber(context.Background(), &api.PhoneMsg{Token: &mover, NewPhone: contested}); err != nil {
		t.Fatalf("change-phone refused a number nobody had proved control of: %s", err.Error())
	}

	code := storedPhoneCode(t, mover.Id)
	if code == "" {
		t.Fatal("no code was stored for the mover")
	}
	if _, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &mover, Code: code}); err != nil {
		t.Fatalf("the mover could not verify the number: %s", err.Error())
	}

	if ci, found := phoneOf(t, mover.Id); !found || ci.ConfirmedAt <= 0 {
		t.Errorf("the mover's number was not confirmed: %+v", ci)
	}
	if _, stillThere := phoneOf(t, claimant.Id); stillThere {
		t.Error("verifying through the change-phone path did not release the stale claim")
	}

	// The other half of the rule: a number somebody has actually proved is still closed, and
	// "phone number already taken" now means exactly that.
	const verifiedElsewhere = "+391230001213"
	owner := addVerifyCodeTestUser(t, "f09_edit_verified_owner@test.com", verifiedElsewhere, models.VerificationCode{}, nil, nil)
	if _, err := testUserDBService.FinalizePhoneVerification(testInstanceID, owner.Id, verifiedElsewhere); err != nil {
		t.Fatalf("failed to seed the verified owner: %s", err.Error())
	}
	blocked := addVerifyCodeTestUser(t, "f09_edit_blocked@test.com", "+391230001214", models.VerificationCode{}, nil, nil)

	_, err := s.EditPhoneNumber(context.Background(), &api.PhoneMsg{Token: &blocked, NewPhone: verifiedElsewhere})
	if ok, msg := shouldHaveGrpcErrorStatus(err, "phone number already taken"); !ok {
		t.Error(msg)
	}
}
