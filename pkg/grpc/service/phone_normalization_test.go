package service

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	api_types "github.com/influenzanet/go-utils/pkg/api_types"
	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/dbs/userdb"
	"github.com/influenzanet/user-management-service/pkg/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Phone numbers are stored and compared in E.164 form, whatever the way they were typed. Every
// test here uses numbers of its own: the test instance is shared by the whole package.

// phoneFormsOf returns different ways of writing the Italian mobile number 347 61 2xxxxx.
func phoneFormsOf(n int) (e164 string, forms []string) {
	e164 = fmt.Sprintf("+39347612%04d", n)
	local := fmt.Sprintf("347 612 %04d", n)
	return e164, []string{
		e164,
		"+39 " + local,
		"0039 " + local,
		fmt.Sprintf("+39-347-612-%04d", n),
		fmt.Sprintf("(+39) 347612%04d", n),
		fmt.Sprintf("0039-347-612-%04d", n),
		fmt.Sprintf("+39 347.612.%04d", n),
		fmt.Sprintf(" +39 347 612 %04d ", n),
	}
}

// newPhoneTestServer is the server of the rate limit tests with the attempts a code allows.
func newPhoneTestServer(client WhatsAppClient) userManagementServer {
	s := newRateLimitTestServer(client)
	s.Intervals.MaxVerificationAttempts = 3
	return s
}

func addPhone(s userManagementServer, token *api_types.TokenInfos, phone string) error {
	_, err := s.AddPhoneNumber(context.Background(), &api.PhoneMsg{Token: token, NewPhone: phone})
	return err
}

// accountsVerifiedOn counts the accounts of the instance that hold the number verified, by
// reading them one by one: the question the uniqueness rule is about.
func accountsVerifiedOn(t *testing.T, tokens []api_types.TokenInfos, phone string) int {
	t.Helper()
	n := 0
	for i := range tokens {
		if ci, found := phoneOf(t, tokens[i].Id); found && ci.Phone == phone && ci.ConfirmedAt > 0 {
			n++
		}
	}
	return n
}

func TestAddPhoneNumberStoresE164(t *testing.T) {
	for i, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{"00 prefix and spaces", "0039 347 612 0100", "+393476120100"},
		{"plus and dashes", "+39-347-612-0101", "+393476120101"},
		{"parenthesised plus", "(+39) 3476120102", "+393476120102"},
		{"UK trunk zero in parentheses", "+44 (0)7911 123456", "+447911123456"},
		{"DE trunk zero", "+49 (0)151 23456789", "+4915123456789"},
		{"IT landline keeps its zero", "+39 06 1234 5678", "+390612345678"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := &countingWhatsAppClient{}
			s := newPhoneTestServer(mock)
			token := addRateLimitTestUser(t, fmt.Sprintf("e164_store_%d@test.com", i), nil)

			if err := addPhone(s, &token, tc.input); err != nil {
				t.Fatalf("unexpected error: %s", err.Error())
			}
			ci, found := phoneOf(t, token.Id)
			if !found || ci.Phone != tc.want {
				t.Fatalf("stored %q, want %q", ci.Phone, tc.want)
			}
			user, err := testUserDBService.GetUserByID(testInstanceID, token.Id)
			if err != nil {
				t.Fatalf("unexpected error: %s", err.Error())
			}
			if user.Account.PhoneVerificationCode.Phone != tc.want {
				t.Errorf("the code is bound to %q, want %q", user.Account.PhoneVerificationCode.Phone, tc.want)
			}
			if mock.recipients[tc.want] != 1 {
				t.Errorf("the code was sent to %v, want %q", mock.recipients, tc.want)
			}
		})
	}
}

func TestAddPhoneNumberRefusesNumbersThatAreNotInternational(t *testing.T) {
	for i, input := range []string{
		"3476120110",         // no international prefix: a region would have to be guessed
		"347 612 0111",       // same, with separators
		"020 7946 0018",      // national format
		"+39 abc",            // letters
		"+39 347",            // too short
		"+999 123456789",     // no such country
		"+0039 347 612 0112", // doubled prefix
		"+39 347 612 0113 5555 5555",
	} {
		t.Run(input, func(t *testing.T) {
			mock := &countingWhatsAppClient{}
			s := newPhoneTestServer(mock)
			token := addRateLimitTestUser(t, fmt.Sprintf("e164_refuse_%d@test.com", i), nil)

			err := addPhone(s, &token, input)
			if status.Code(err) != codes.InvalidArgument || status.Convert(err).Message() != "phone not valid" {
				t.Fatalf("unexpected answer: %v", err)
			}
			if _, found := phoneOf(t, token.Id); found {
				t.Error("an invalid number was stored")
			}
			if mock.count() != 0 {
				t.Error("an invalid number reached Meta")
			}
		})
	}
}

func TestAddPhoneNumberSameNumberInAnotherFormIsTaken(t *testing.T) {
	e164, forms := phoneFormsOf(200)
	s := newPhoneTestServer(&countingWhatsAppClient{})
	seedPhoneOwner(t, "e164_owner@test.com", e164)

	for i, form := range forms {
		token := addRateLimitTestUser(t, fmt.Sprintf("e164_taken_%d@test.com", i), nil)
		err := addPhone(s, &token, form)
		if status.Code(err) != codes.InvalidArgument || status.Convert(err).Message() != "phone number already taken" {
			t.Errorf("form %q: unexpected answer: %v", form, err)
		}
		if _, found := phoneOf(t, token.Id); found {
			t.Errorf("form %q: the number was stored on a second account", form)
		}
	}
}

func TestAddPhoneNumberFirstAddedThenAnotherFormByTheSecond(t *testing.T) {
	// The scenario of the report, end to end: 0039 form first, the +39 form on a second account.
	mock := &countingWhatsAppClient{}
	s := newPhoneTestServer(mock)
	first := addRateLimitTestUser(t, "e164_flow_a@test.com", nil)
	second := addRateLimitTestUser(t, "e164_flow_b@test.com", nil)

	if err := addPhone(s, &first, "0039 347 612 0300"); err != nil {
		t.Fatalf("unexpected error: %s", err.Error())
	}
	if ci, _ := phoneOf(t, first.Id); ci.Phone != "+393476120300" {
		t.Fatalf("stored %q", ci.Phone)
	}
	user, _ := testUserDBService.GetUserByID(testInstanceID, first.Id)
	if _, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{
		Token: &first, Code: user.Account.PhoneVerificationCode.Code,
	}); err != nil {
		t.Fatalf("verification failed: %s", err.Error())
	}

	err := addPhone(s, &second, "+39 347 612 0300")
	if status.Code(err) != codes.InvalidArgument || status.Convert(err).Message() != "phone number already taken" {
		t.Fatalf("unexpected answer: %v", err)
	}
	if _, found := phoneOf(t, second.Id); found {
		t.Error("the number was stored on a second account")
	}
}

func TestAddPhoneNumberAgainInAnotherFormIsTheSameNumber(t *testing.T) {
	mock := &countingWhatsAppClient{}
	s := newPhoneTestServer(mock)
	token := addRateLimitTestUser(t, "e164_resend_same@test.com", nil)

	if err := addPhone(s, &token, "+39 347 612 0400"); err != nil {
		t.Fatalf("unexpected error: %s", err.Error())
	}
	first, _ := phoneOf(t, token.Id)

	// The same telephone written another way is a resend, not a second number
	if err := addPhone(s, &token, "0039 3476120400"); err != nil {
		t.Fatalf("the same number in another form was refused: %s", err.Error())
	}
	again, _ := phoneOf(t, token.Id)
	if again.ID != first.ID || again.Phone != "+393476120400" {
		t.Errorf("the contact changed: %+v then %+v", first, again)
	}

	// A different number is still refused
	err := addPhone(s, &token, "+39 347 612 0401")
	if status.Code(err) != codes.InvalidArgument || status.Convert(err).Message() != "user already has a phone number" {
		t.Errorf("unexpected answer: %v", err)
	}
}

func TestEditPhoneNumberComparesNormalisedValues(t *testing.T) {
	mock := &countingWhatsAppClient{}
	s := newPhoneTestServer(mock)
	edit := func(token *api_types.TokenInfos, phone string) error {
		_, err := s.EditPhoneNumber(context.Background(), &api.PhoneMsg{Token: token, NewPhone: phone})
		return err
	}

	t.Run("same number in another form keeps the contact and resends", func(t *testing.T) {
		token := addRateLimitTestUser(t, "e164_edit_same@test.com", nil)
		if err := addPhone(s, &token, "+39 347 612 0500"); err != nil {
			t.Fatal(err)
		}
		before, _ := phoneOf(t, token.Id)
		if err := edit(&token, "0039 347 612 0500"); err != nil {
			t.Fatalf("unexpected error: %s", err.Error())
		}
		after, _ := phoneOf(t, token.Id)
		if after.ID != before.ID || after.Phone != "+393476120500" {
			t.Errorf("the contact was replaced: %+v then %+v", before, after)
		}
	})

	t.Run("verified number is not edited into itself", func(t *testing.T) {
		token := addVerifyCodeTestUser(t, "e164_edit_verified@test.com", "+393476120510", models.VerificationCode{}, nil, nil)
		if _, err := testUserDBService.FinalizePhoneVerification(testInstanceID, token.Id, "+393476120510"); err != nil {
			t.Fatal(err)
		}
		err := edit(&token, "0039 347 612 0510")
		if status.Code(err) != codes.InvalidArgument || status.Convert(err).Message() != "phone number already verified" {
			t.Errorf("unexpected answer: %v", err)
		}
	})

	t.Run("another account's verified number is taken in any form", func(t *testing.T) {
		seedPhoneOwner(t, "e164_edit_owner@test.com", "+393476120520")
		token := addVerifyCodeTestUser(t, "e164_edit_taker@test.com", "+393476120521", models.VerificationCode{}, nil, nil)
		err := edit(&token, "0039 347 612 0520")
		if status.Code(err) != codes.InvalidArgument || status.Convert(err).Message() != "phone number already taken" {
			t.Errorf("unexpected answer: %v", err)
		}
		if ci, _ := phoneOf(t, token.Id); ci.Phone != "+393476120521" {
			t.Errorf("the contact changed to %q", ci.Phone)
		}
	})

	t.Run("a new number is stored normalised", func(t *testing.T) {
		token := addVerifyCodeTestUser(t, "e164_edit_new@test.com", "+393476120530", models.VerificationCode{}, nil, nil)
		if err := edit(&token, "+44 (0)7911 123457"); err != nil {
			t.Fatalf("unexpected error: %s", err.Error())
		}
		if ci, _ := phoneOf(t, token.Id); ci.Phone != "+447911123457" {
			t.Errorf("stored %q", ci.Phone)
		}
	})
}

func TestVerifyAfterTheNumberWasEnteredWithSeparators(t *testing.T) {
	mock := &countingWhatsAppClient{}
	s := newPhoneTestServer(mock)
	token := addRateLimitTestUser(t, "e164_verify@test.com", nil)

	if err := addPhone(s, &token, "0039 347 612 0600"); err != nil {
		t.Fatalf("unexpected error: %s", err.Error())
	}
	user, _ := testUserDBService.GetUserByID(testInstanceID, token.Id)
	_, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{
		Token: &token, Code: user.Account.PhoneVerificationCode.Code,
	})
	if err != nil {
		t.Fatalf("verification failed: %s", err.Error())
	}
	ci, _ := phoneOf(t, token.Id)
	if ci.ConfirmedAt <= 0 || ci.Phone != "+393476120600" {
		t.Errorf("unexpected contact: %+v", ci)
	}
	if !hasChannel(channelsOf(t, token.Id), models.ChannelWhatsApp) {
		t.Error("the whatsapp channel was not enabled")
	}
}

func TestResendContactVerificationMatchesTheAddressAsTheSameNumber(t *testing.T) {
	mock := &countingWhatsAppClient{}
	s := newPhoneTestServer(mock)
	users, err := addTestUsers([]models.User{{
		Account: models.Account{Type: "email", AccountID: "e164_resend@test.com", AccountConfirmedAt: time.Now().Unix()},
		ContactInfos: []models.ContactInfo{
			{ID: primitive.NewObjectID(), Type: models.ContactTypeEmail, Email: "e164_resend@test.com", ConfirmedAt: time.Now().Unix()},
			{ID: primitive.NewObjectID(), Type: models.ContactTypePhone, Phone: "+393476120700"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	token := &api_types.TokenInfos{Id: users[0].ID.Hex(), InstanceId: testInstanceID}

	_, err = s.ResendContactVerification(context.Background(), &api.ResendContactVerificationReq{
		Token: token, Type: models.ContactTypePhone, Address: "0039 347 612 0700",
	})
	if err != nil {
		t.Fatalf("unexpected error: %s", err.Error())
	}
	if mock.recipients["+393476120700"] != 1 {
		t.Errorf("the code was sent to %v", mock.recipients)
	}
	user, _ := testUserDBService.GetUserByID(testInstanceID, token.Id)
	if user.Account.PhoneVerificationCode.Phone != "+393476120700" {
		t.Errorf("the code is bound to %q", user.Account.PhoneVerificationCode.Phone)
	}
	// ... and the code it issued verifies the number
	if _, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{
		Token: token, Code: user.Account.PhoneVerificationCode.Code,
	}); err != nil {
		t.Fatalf("verification failed: %s", err.Error())
	}
}

func TestSignupWithEmailStoresE164AndComparesNormalisedValues(t *testing.T) {
	s := newSignupTestServer(t)
	seedPhoneOwner(t, "e164_signup_owner@test.com", "+393476120800")

	if _, err := s.SignupWithEmail(context.Background(), signupReq("e164_signup_new@test.com", "0039 347 612 0801")); err != nil {
		t.Fatalf("unexpected error: %s", err.Error())
	}
	user, err := testUserDBService.GetUserByAccountID(testInstanceID, "e164_signup_new@test.com")
	if err != nil {
		t.Fatal(err)
	}
	if ci, found := user.FindContactInfoByTypeAndAddr(models.ContactTypePhone, "+393476120801"); !found || ci.ConfirmedAt != 0 {
		t.Errorf("the number was not stored normalised and unverified: %+v", user.ContactInfos)
	}

	// The number of an existing participant, in another form, is refused like a duplicate account
	_, err = s.SignupWithEmail(context.Background(), signupReq("e164_signup_probe@test.com", "+39 347 612 0800"))
	if status.Code(err) != codes.Internal || status.Convert(err).Message() != "user creation failed" {
		t.Errorf("unexpected answer: %v", err)
	}

	// A number without an international prefix is not valid
	_, err = s.SignupWithEmail(context.Background(), signupReq("e164_signup_noprefix@test.com", "3476120802"))
	if status.Code(err) != codes.InvalidArgument || status.Convert(err).Message() != "phone not valid" {
		t.Errorf("unexpected answer: %v", err)
	}
}

// Two accounts proving the same number: whichever way the requests interleave, the database
// lets one of them through. The deterministic version of the race calls the two finalisations
// back to back, the way two overlapping requests would, without the release that
// VerifyWhatsAppCode runs afterwards.
func TestFinalizePhoneVerificationOnlyOneAccountHoldsTheNumber(t *testing.T) {
	const phone = "+393476120900"
	a := addVerifyCodeTestUser(t, "e164_final_a@test.com", phone, validCode("700001"), nil, nil)
	b := addVerifyCodeTestUser(t, "e164_final_b@test.com", phone, validCode("700002"), nil, nil)

	if _, err := testUserDBService.FinalizePhoneVerification(testInstanceID, a.Id, phone); err != nil {
		t.Fatalf("the first account could not verify: %s", err.Error())
	}
	_, err := testUserDBService.FinalizePhoneVerification(testInstanceID, b.Id, phone)
	if err != userdb.ErrPhoneVerifiedElsewhere {
		t.Fatalf("the second account was not refused: %v", err)
	}
	if ci, _ := phoneOf(t, b.Id); ci.ConfirmedAt != 0 {
		t.Errorf("the refused account was confirmed: %+v", ci)
	}
	// The same account confirming again is idempotent
	if _, err := testUserDBService.FinalizePhoneVerification(testInstanceID, a.Id, phone); err != nil {
		t.Errorf("a repeated finalisation failed: %s", err.Error())
	}

	// The endpoint answers the loser as a number that is taken
	s := newPhoneTestServer(&countingWhatsAppClient{})
	_, err = s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &b, Code: "700002"})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("unexpected answer: %v", err)
	}
}

func TestFinalizePhoneVerificationTakesOverAStaleClaim(t *testing.T) {
	const phone = "+393476120910"
	a := addVerifyCodeTestUser(t, "e164_stale_a@test.com", phone, validCode("710001"), nil, nil)
	b := addVerifyCodeTestUser(t, "e164_stale_b@test.com", phone, validCode("710002"), nil, nil)

	if _, err := testUserDBService.FinalizePhoneVerification(testInstanceID, a.Id, phone); err != nil {
		t.Fatal(err)
	}
	// The holder lets the number go: the claim it left behind must not hold the number for good
	if _, err := testUserDBService.DeletePhoneNumber(testInstanceID, a.Id); err != nil {
		t.Fatal(err)
	}
	if _, err := testUserDBService.FinalizePhoneVerification(testInstanceID, b.Id, phone); err != nil {
		t.Fatalf("a stale claim kept the number from its next owner: %s", err.Error())
	}
	if ci, _ := phoneOf(t, b.Id); ci.ConfirmedAt <= 0 {
		t.Errorf("not confirmed: %+v", ci)
	}
}

func TestFinalizePhoneVerificationClaimIsDroppedWhenTheConfirmationFails(t *testing.T) {
	const phone = "+393476120920"
	a := addVerifyCodeTestUser(t, "e164_drop_a@test.com", phone, validCode("720001"), nil, nil)
	b := addVerifyCodeTestUser(t, "e164_drop_b@test.com", phone, validCode("720002"), nil, nil)

	// A's number moved: finalising the old one confirms nothing and must not claim anything
	if _, err := testUserDBService.DeletePhoneNumber(testInstanceID, a.Id); err != nil {
		t.Fatal(err)
	}
	if _, err := testUserDBService.FinalizePhoneVerification(testInstanceID, a.Id, phone); err == nil {
		t.Fatal("expected a failure")
	}
	if _, err := testUserDBService.FinalizePhoneVerification(testInstanceID, b.Id, phone); err != nil {
		t.Fatalf("a failed confirmation kept the number from another account: %s", err.Error())
	}
}

// Burst: many accounts, each adding the same number written differently, then proving it all at
// once. Adding is allowed to succeed on all of them (an unverified claim holds nothing); what
// must hold is that at most one account ends up with the number verified.
func TestSameNumberInDifferentFormsBurstEndsOnOneAccount(t *testing.T) {
	const accounts = 8
	e164, forms := phoneFormsOf(1000)
	mock := &countingWhatsAppClient{}
	s := newPhoneTestServer(mock)

	tokens := make([]api_types.TokenInfos, accounts)
	for i := range tokens {
		tokens[i] = addRateLimitTestUser(t, fmt.Sprintf("e164_burst_%d@test.com", i), nil)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range tokens {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if err := addPhone(s, &tokens[i], forms[i%len(forms)]); err != nil {
				t.Errorf("account %d (%q): unexpected error: %s", i, forms[i%len(forms)], err.Error())
			}
		}(i)
	}
	close(start)
	wg.Wait()

	verifyCodes := make([]string, accounts)
	for i := range tokens {
		ci, found := phoneOf(t, tokens[i].Id)
		if !found || ci.Phone != e164 {
			t.Fatalf("account %d stored %q, want %q", i, ci.Phone, e164)
		}
		user, _ := testUserDBService.GetUserByID(testInstanceID, tokens[i].Id)
		verifyCodes[i] = user.Account.PhoneVerificationCode.Code
	}

	start = make(chan struct{})
	for i := range tokens {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, _ = s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &tokens[i], Code: verifyCodes[i]})
		}(i)
	}
	close(start)
	wg.Wait()

	if verified := accountsVerifiedOn(t, tokens, e164); verified != 1 {
		t.Fatalf("%d accounts hold the number verified, want exactly 1", verified)
	}
	// Late comers, in yet other forms, are told it is taken
	for i, form := range forms {
		late := addRateLimitTestUser(t, fmt.Sprintf("e164_burst_late_%d@test.com", i), nil)
		err := addPhone(s, &late, form)
		if status.Code(err) != codes.InvalidArgument || status.Convert(err).Message() != "phone number already taken" {
			t.Errorf("late account with %q: unexpected answer: %v", form, err)
		}
	}
}

// Staggered: the same, with the accounts started a few milliseconds apart, so that some of them
// find the number already verified when they add it and the others still race to prove it.
func TestSameNumberInDifferentFormsStaggeredEndsOnOneAccount(t *testing.T) {
	const accounts = 8
	e164, forms := phoneFormsOf(1100)
	s := newPhoneTestServer(&countingWhatsAppClient{})

	tokens := make([]api_types.TokenInfos, accounts)
	for i := range tokens {
		tokens[i] = addRateLimitTestUser(t, fmt.Sprintf("e164_stagger_%d@test.com", i), nil)
	}

	var wg sync.WaitGroup
	for i := range tokens {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			time.Sleep(time.Duration(i) * 15 * time.Millisecond)
			err := addPhone(s, &tokens[i], forms[i%len(forms)])
			if err != nil {
				// Allowed only as "taken": an earlier account has already proved the number
				if status.Convert(err).Message() != "phone number already taken" {
					t.Errorf("account %d: unexpected error: %s", i, err.Error())
				}
				return
			}
			user, _ := testUserDBService.GetUserByID(testInstanceID, tokens[i].Id)
			_, _ = s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{
				Token: &tokens[i], Code: user.Account.PhoneVerificationCode.Code,
			})
		}(i)
	}
	wg.Wait()

	if verified := accountsVerifiedOn(t, tokens, e164); verified != 1 {
		t.Fatalf("%d accounts hold the number verified, want exactly 1", verified)
	}
	for i := range tokens {
		if ci, found := phoneOf(t, tokens[i].Id); found && ci.Phone != e164 {
			t.Errorf("account %d stored %q", i, ci.Phone)
		}
	}
}

// Burst with stale claims: the claim collection already names accounts that hold the number
// unverified (one of the contenders itself, and an account that no longer exists), the way it is
// left by a number that was verified, let go and added again. All contenders then prove the number
// at once; the claim is taken over by one of them while the stale owner may be confirming too.
// Whatever the order, at most one account ends up with the number verified, and the accounts that
// lost it got nothing from the attempt.
func TestSameNumberBurstWithStaleClaimsEndsOnOneAccount(t *testing.T) {
	const accounts = 8
	const rounds = 5

	for round := 0; round < rounds; round++ {
		e164, _ := phoneFormsOf(1300 + round)
		s := newPhoneTestServer(&countingWhatsAppClient{})

		tokens := make([]api_types.TokenInfos, accounts)
		codes := make([]string, accounts)
		for i := range tokens {
			code := fmt.Sprintf("%06d", 800000+round*100+i)
			tokens[i] = addVerifyCodeTestUser(t, fmt.Sprintf("e164_stale_burst_%d_%d@test.com", round, i), e164, validCode(code), nil, nil)
			codes[i] = code
		}

		owner := tokens[round%accounts].Id
		if round%2 == 1 {
			owner = primitive.NewObjectID().Hex() // the owner of the claim is gone
		}
		ownerID, err := primitive.ObjectIDFromHex(owner)
		if err != nil {
			t.Fatal(err)
		}
		claims := testUserDBService.DBClient.Database(testUserDBService.DBNamePrefix + testInstanceID + "_users").Collection(userdb.VerifiedPhoneCollection)
		if _, err := claims.InsertOne(context.Background(), bson.M{"_id": e164, "userId": ownerID, "claimedAt": time.Now().Unix()}); err != nil {
			t.Fatal(err)
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range tokens {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				_, _ = s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &tokens[i], Code: codes[i]})
			}(i)
		}
		close(start)
		wg.Wait()

		if verified := accountsVerifiedOn(t, tokens, e164); verified != 1 {
			t.Fatalf("round %d: %d accounts hold the number verified, want exactly 1", round, verified)
		}
		// The claim names the account that holds the number, and nobody else has the channel
		var claim struct {
			UserID primitive.ObjectID `bson:"userId"`
		}
		if err := claims.FindOne(context.Background(), bson.M{"_id": e164}).Decode(&claim); err != nil {
			t.Fatal(err)
		}
		for i := range tokens {
			ci, _ := phoneOf(t, tokens[i].Id)
			if (ci.ConfirmedAt > 0) != (tokens[i].Id == claim.UserID.Hex()) {
				t.Errorf("round %d: account %d confirmed=%v but the claim names %s", round, i, ci.ConfirmedAt > 0, claim.UserID.Hex())
			}
			if ci.ConfirmedAt == 0 && hasChannel(channelsOf(t, tokens[i].Id), models.ChannelWhatsApp) {
				t.Errorf("round %d: account %d has the whatsapp channel without the number", round, i)
			}
		}
	}
}
