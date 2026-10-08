package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/influenzanet/user-management-service/pkg/dbs/userdb"
	"github.com/influenzanet/user-management-service/pkg/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

const testDBNamePrefix = "TEST_NORMALIZE_PHONES_"

var (
	testUserDBService *userdb.UserDBService
	testRunID         = strconv.FormatInt(time.Now().Unix(), 10)
	testInstances     []string
)

func TestMain(m *testing.M) {
	URI := fmt.Sprintf("mongodb%s://%s:%s@%s", os.Getenv("USER_DB_CONNECTION_PREFIX"),
		os.Getenv("USER_DB_USERNAME"), os.Getenv("USER_DB_PASSWORD"), os.Getenv("USER_DB_CONNECTION_STR"))
	testUserDBService = userdb.NewUserDBService(models.DBConfig{
		URI: URI, Timeout: 30, IdleConnTimeout: 45, MaxPoolSize: 8, DBNamePrefix: testDBNamePrefix,
	})
	code := m.Run()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, instance := range testInstances {
		_ = testUserDBService.DBClient.Database(testDBNamePrefix + instance + "_users").Drop(ctx)
	}
	os.Exit(code)
}

// freshInstance gives a test an instance of its own, so tests cannot see each other's accounts.
func freshInstance(t *testing.T) string {
	t.Helper()
	instance := fmt.Sprintf("%s_%d", testRunID, len(testInstances))
	testInstances = append(testInstances, instance)
	return instance
}

func insertUser(t *testing.T, instance string, phone string, confirmedAt int64, codePhone string) primitive.ObjectID {
	t.Helper()
	id := primitive.NewObjectID()
	doc := bson.M{
		"_id": id,
		"contactInfos": bson.A{
			bson.M{"_id": primitive.NewObjectID(), "type": "email", "email": id.Hex() + "@test.com", "confirmedAt": int64(1)},
		},
		"account": bson.M{"phoneVerificationCode": bson.M{"code": "123456"}},
	}
	if phone != "" {
		doc["contactInfos"] = append(doc["contactInfos"].(bson.A), bson.M{"_id": primitive.NewObjectID(), "type": "phone", "phone": phone, "confirmedAt": confirmedAt})
	}
	if codePhone != "" {
		doc["account"] = bson.M{"phoneVerificationCode": bson.M{"code": "123456", "phone": codePhone}}
	}
	ctx, cancel := testUserDBService.GetContext()
	defer cancel()
	if _, err := testUserDBService.GetCollection(instance, userdb.UserCollection).InsertOne(ctx, doc); err != nil {
		t.Fatalf("seeding failed: %v", err)
	}
	return id
}

func storedPhones(t *testing.T, instance string, id primitive.ObjectID) (contact string, code string) {
	t.Helper()
	ctx, cancel := testUserDBService.GetContext()
	defer cancel()
	var u phoneUser
	if err := testUserDBService.GetCollection(instance, userdb.UserCollection).FindOne(ctx, bson.M{"_id": id}).Decode(&u); err != nil {
		t.Fatalf("read failed: %v", err)
	}
	for _, ci := range u.ContactInfos {
		if ci.Type == "phone" {
			contact = ci.Phone
		}
	}
	return contact, u.Account.PhoneVerificationCode.Phone
}

func TestDryRunWritesNothing(t *testing.T) {
	instance := freshInstance(t)
	id := insertUser(t, instance, "+44 (0)20 7946 0018", 0, "+44 (0)20 7946 0018")

	report, err := NormalizePhoneNumbers(testUserDBService, instance, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Changes) != 2 || report.Changed != 0 || report.Applied {
		t.Errorf("unexpected report: %+v", report)
	}
	contact, code := storedPhones(t, instance, id)
	if contact != "+44 (0)20 7946 0018" || code != "+44 (0)20 7946 0018" {
		t.Errorf("a dry run modified the data: %q %q", contact, code)
	}
	// The report carries masked numbers only
	for _, c := range report.Changes {
		if c.Old != "+44***0018" || c.New != "+44***0018" {
			t.Errorf("numbers are not masked: %+v", c)
		}
	}
}

func TestApplyNormalizesAndIsIdempotent(t *testing.T) {
	instance := freshInstance(t)
	ukBoth := insertUser(t, instance, "+4402079460018", 0, "+4402079460018")
	itCodeOnly := insertUser(t, instance, "+393316221419", 5, "0039 331 6221419")
	deContactOnly := insertUser(t, instance, "+49030123456", 7, "")
	insertUser(t, instance, "+4402079460018x", 0, "")
	already := insertUser(t, instance, "+34612345678", 9, "")
	none := insertUser(t, instance, "", 0, "")

	first, err := NormalizePhoneNumbers(testUserDBService, instance, true)
	if err != nil {
		t.Fatal(err)
	}
	if first.Changed != 3 || first.SkippedConcurrent != 0 || first.AlreadyNormalized != 2 {
		t.Errorf("unexpected first report: %+v", first)
	}
	if len(first.Unparseable) != 1 {
		t.Errorf("the unparseable number was not reported: %+v", first.Unparseable)
	}

	for _, tc := range []struct {
		id            primitive.ObjectID
		contact, code string
	}{
		{ukBoth, "+442079460018", "+442079460018"},
		{itCodeOnly, "+393316221419", "+393316221419"},
		{deContactOnly, "+4930123456", ""},
		{already, "+34612345678", ""},
		{none, "", ""},
	} {
		contact, code := storedPhones(t, instance, tc.id)
		if contact != tc.contact || code != tc.code {
			t.Errorf("user %s: got %q %q, want %q %q", tc.id.Hex(), contact, code, tc.contact, tc.code)
		}
	}

	// Verification state must survive the rewrite
	ctx, cancel := testUserDBService.GetContext()
	defer cancel()
	var u phoneUser
	if err := testUserDBService.GetCollection(instance, userdb.UserCollection).FindOne(ctx, bson.M{"_id": itCodeOnly}).Decode(&u); err != nil {
		t.Fatal(err)
	}
	if len(u.ContactInfos) != 2 || u.ContactInfos[1].ConfirmedAt != 5 || u.ContactInfos[0].Type != "email" {
		t.Errorf("the rewrite damaged the other contact data: %+v", u.ContactInfos)
	}

	second, err := NormalizePhoneNumbers(testUserDBService, instance, true)
	if err != nil {
		t.Fatal(err)
	}
	if second.Changed != 0 || len(second.Changes) != 0 || second.AlreadyNormalized != 4 {
		t.Errorf("the second run is not a no-op: %+v", second)
	}
}

func TestTwoVerifiedHoldersAreReportedAndLeftUntouched(t *testing.T) {
	instance := freshInstance(t)
	a := insertUser(t, instance, "+39 331 622 1419", 3, "")
	b := insertUser(t, instance, "+393316221419", 4, "")
	c := insertUser(t, instance, "+4402079460018", 0, "")
	free := insertUser(t, instance, "+4930 123456", 0, "")

	for _, apply := range []bool{false, true, true} {
		report, err := NormalizePhoneNumbers(testUserDBService, instance, apply)
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Collisions) != 1 {
			t.Fatalf("apply=%v: expected one collision, got %+v", apply, report.Collisions)
		}
		col := report.Collisions[0]
		if len(col.UserIDs) != 2 || col.Verified != 2 || col.Number != "+39***1419" {
			t.Errorf("apply=%v: unexpected collision: %+v", apply, col)
		}
		for _, ch := range report.Changes {
			if ch.UserID == a.Hex() || ch.UserID == b.Hex() {
				t.Errorf("apply=%v: a colliding account is in the changes: %+v", apply, ch)
			}
		}
	}
	if contact, _ := storedPhones(t, instance, a); contact != "+39 331 622 1419" {
		t.Errorf("colliding account modified: %q", contact)
	}
	if contact, _ := storedPhones(t, instance, b); contact != "+393316221419" {
		t.Errorf("colliding account modified: %q", contact)
	}
	if contact, _ := storedPhones(t, instance, c); contact != "+442079460018" {
		t.Errorf("a non colliding account was not normalised: %q", contact)
	}
	if contact, _ := storedPhones(t, instance, free); contact != "+4930123456" {
		t.Errorf("a non colliding account was not normalised: %q", contact)
	}
	// Nobody gets a claim on a number two accounts hold verified
	if _, found := claimOf(t, instance, "+393316221419"); found {
		t.Error("a claim was seeded for a colliding number")
	}
}

func claimOf(t *testing.T, instance, number string) (primitive.ObjectID, bool) {
	t.Helper()
	ctx, cancel := testUserDBService.GetContext()
	defer cancel()
	var claim struct {
		UserID primitive.ObjectID `bson:"userId"`
	}
	err := testUserDBService.GetCollection(instance, userdb.VerifiedPhoneCollection).FindOne(ctx, bson.M{"_id": number}).Decode(&claim)
	return claim.UserID, err == nil
}

func TestSharedUnverifiedNumberIsNormalised(t *testing.T) {
	instance := freshInstance(t)
	a := insertUser(t, instance, "+39 331 622 1419", 0, "")
	b := insertUser(t, instance, "+393316221419", 0, "")
	c := insertUser(t, instance, "0039 331 6221419", 0, "")

	report, err := NormalizePhoneNumbers(testUserDBService, instance, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Collisions) != 0 || len(report.Notes) != 0 || report.Changed != 2 {
		t.Errorf("a number nobody verified was treated as a problem: %+v", report)
	}
	for _, id := range []primitive.ObjectID{a, b, c} {
		if contact, _ := storedPhones(t, instance, id); contact != "+393316221419" {
			t.Errorf("not normalised: %q", contact)
		}
	}
	if _, found := claimOf(t, instance, "+393316221419"); found {
		t.Error("a claim was seeded for a number nobody holds verified")
	}
}

func TestOneVerifiedHolderAndUnverifiedOnesAreNormalisedWithANote(t *testing.T) {
	instance := freshInstance(t)
	holder := insertUser(t, instance, "+39 331 622 1419", 3, "")
	other := insertUser(t, instance, "0039 331 6221419", 0, "")

	for _, apply := range []bool{false, true} {
		report, err := NormalizePhoneNumbers(testUserDBService, instance, apply)
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Collisions) != 0 {
			t.Fatalf("apply=%v: reported as a collision: %+v", apply, report.Collisions)
		}
		if len(report.Notes) != 1 || report.Notes[0].Verified != holder.Hex() || len(report.Notes[0].UserIDs) != 2 || report.Notes[0].Number != "+39***1419" {
			t.Fatalf("apply=%v: unexpected notes: %+v", apply, report.Notes)
		}
		if len(report.Changes) != 2 && apply == false {
			t.Errorf("both accounts must be in the changes: %+v", report.Changes)
		}
	}
	for _, id := range []primitive.ObjectID{holder, other} {
		if contact, _ := storedPhones(t, instance, id); contact != "+393316221419" {
			t.Errorf("not normalised: %q", contact)
		}
	}
	if owner, found := claimOf(t, instance, "+393316221419"); !found || owner != holder {
		t.Errorf("the claim does not name the verified holder: %v %v", owner, found)
	}
}

func TestClaimsAreSeededForVerifiedHoldersOnly(t *testing.T) {
	instance := freshInstance(t)
	legacy := insertUser(t, instance, "+4402079460018", 5, "")
	plain := insertUser(t, instance, "+34612345678", 6, "")
	unverified := insertUser(t, instance, "+4930 123456", 0, "")
	taken := insertUser(t, instance, "+39 331 622 1419", 7, "")
	other := primitive.NewObjectID()
	ctx, cancel := testUserDBService.GetContext()
	defer cancel()
	if _, err := testUserDBService.GetCollection(instance, userdb.VerifiedPhoneCollection).InsertOne(ctx,
		bson.M{"_id": "+393316221419", "userId": other, "claimedAt": int64(1)}); err != nil {
		t.Fatal(err)
	}

	dry, err := NormalizePhoneNumbers(testUserDBService, instance, false)
	if err != nil {
		t.Fatal(err)
	}
	if dry.ClaimsToSeed != 2 || dry.ClaimsSeeded != 0 || len(dry.ClaimConflicts) != 1 {
		t.Errorf("unexpected dry run: %+v", dry)
	}
	if _, found := claimOf(t, instance, "+442079460018"); found {
		t.Fatal("a dry run seeded a claim")
	}

	for i := 0; i < 2; i++ {
		report, err := NormalizePhoneNumbers(testUserDBService, instance, true)
		if err != nil {
			t.Fatal(err)
		}
		wantSeeded, wantPresent := 2, 0
		if i == 1 {
			wantSeeded, wantPresent = 0, 2
		}
		if report.ClaimsSeeded != wantSeeded || report.ClaimsPresent != wantPresent || len(report.ClaimConflicts) != 1 {
			t.Errorf("run %d: unexpected report: %+v", i, report)
		}
		if c := report.ClaimConflicts[0]; c.UserID != taken.Hex() || c.OwnerID != other.Hex() || c.Number != "+39***1419" {
			t.Errorf("run %d: unexpected conflict: %+v", i, c)
		}
	}
	if owner, found := claimOf(t, instance, "+442079460018"); !found || owner != legacy {
		t.Errorf("legacy spelling: claim %v %v", owner, found)
	}
	if owner, found := claimOf(t, instance, "+34612345678"); !found || owner != plain {
		t.Errorf("claim %v %v", owner, found)
	}
	if _, found := claimOf(t, instance, "+4930123456"); found {
		t.Errorf("a claim was seeded for unverified %s", unverified.Hex())
	}
	if owner, _ := claimOf(t, instance, "+393316221419"); owner != other {
		t.Errorf("a claim of another account was overwritten: %v", owner)
	}
}

func TestReportMakesATrunkZeroFixVisible(t *testing.T) {
	instance := freshInstance(t)
	insertUser(t, instance, "+4402079460018", 0, "")
	insertUser(t, instance, "0044 20 7946 0019", 0, "")
	insertUser(t, instance, "+34 612 345 678", 0, "")

	report, err := NormalizePhoneNumbers(testUserDBService, instance, false)
	if err != nil {
		t.Fatal(err)
	}
	byOld := map[string]Change{}
	for _, c := range report.Changes {
		byOld[c.Old] = c
	}
	trunk := byOld["+44***0018"]
	if !trunk.TrunkZeroRemoved || trunk.OldDigits != 13 || trunk.NewDigits != 12 {
		t.Errorf("trunk zero not visible: %+v", trunk)
	}
	prefix := byOld["004***0019"]
	if prefix.TrunkZeroRemoved || prefix.OldDigits != 14 || prefix.NewDigits != 12 {
		t.Errorf("the 00 prefix was read as a trunk zero: %+v", prefix)
	}
	spaced := byOld["+34***5678"]
	if spaced.TrunkZeroRemoved {
		t.Errorf("separators were read as a trunk zero: %+v", spaced)
	}
}

func TestParseParams(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		apply   bool
		wantErr string
	}{
		{"dry run by default", []string{"-instance=a"}, false, ""},
		{"explicit dry run", []string{"-instance=a", "-dry-run"}, false, ""},
		{"apply", []string{"-instance=a", "-apply"}, true, ""},
		{"apply with dry-run=false", []string{"-instance=a", "-apply", "-dry-run=false"}, true, ""},
		{"apply with dry-run", []string{"-instance=a", "-apply", "-dry-run=true"}, false, "cannot be combined"},
		{"dry-run=false alone", []string{"-instance=a", "-dry-run=false"}, false, "add -apply"},
		{"no instance", []string{"-apply"}, false, "instance must be provided"},
		{"unknown flag", []string{"-instance=a", "-force"}, false, "not defined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := parseParams(tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected an error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil || p.apply != tc.apply || len(p.instances) != 1 {
				t.Errorf("unexpected result: %+v %v", p, err)
			}
		})
	}
}

func TestApplyDoesNotOverwriteAConcurrentChange(t *testing.T) {
	instance := freshInstance(t)
	id := insertUser(t, instance, "+4402079460018", 0, "")

	// The plan is computed and then the number changes before the write lands.
	plan := &userPlan{id: id, contactSeen: true, contactOld: "+4402079460018", contactNew: "+442079460018"}
	ctx, cancel := testUserDBService.GetContext()
	defer cancel()
	if _, err := testUserDBService.GetCollection(instance, userdb.UserCollection).UpdateOne(ctx, bson.M{"_id": id},
		bson.M{"$set": bson.M{"contactInfos.1.phone": "+34612345678"}}); err != nil {
		t.Fatal(err)
	}
	matched, err := writePlan(testUserDBService, instance, plan, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if matched {
		t.Error("the write matched although the stored number had changed")
	}
	if contact, _ := storedPhones(t, instance, id); contact != "+34612345678" {
		t.Errorf("a concurrent change was overwritten: %q", contact)
	}
}

func TestStaleCodeBoundToAnotherNumberIsNormalisedOnItsOwn(t *testing.T) {
	instance := freshInstance(t)
	id := insertUser(t, instance, "+442079460018", 0, "0044 20 7946 0018")

	report, err := NormalizePhoneNumbers(testUserDBService, instance, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Changed != 1 {
		t.Errorf("unexpected report: %+v", report)
	}
	contact, code := storedPhones(t, instance, id)
	if contact != "+442079460018" || code != "+442079460018" {
		t.Errorf("got %q %q", contact, code)
	}
}
