package main

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/influenzanet/user-management-service/pkg/dbs/userdb"
	"github.com/influenzanet/user-management-service/pkg/models"
	"github.com/influenzanet/user-management-service/pkg/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Where a phone number was found on an account.
const (
	fieldContact = "contactInfos"
	fieldCode    = "account.phoneVerificationCode"
)

// Change is one stored number that is not in E.164 form. Numbers are masked: the report ends up
// in terminals and logs. A masked number keeps only its first and last digits, so a change that
// only drops the trunk zero looks the same before and after; the digit counts and
// TrunkZeroRemoved make it visible without unmasking anything.
type Change struct {
	UserID           string
	Field            string
	Old              string
	New              string
	OldDigits        int
	NewDigits        int
	TrunkZeroRemoved bool
}

// Unparseable is a stored number that cannot be turned into a valid E.164 number. It is left
// untouched.
type Unparseable struct {
	UserID string
	Field  string
	Number string
}

// Collision is a group of accounts of which at least two hold the same number, once normalised,
// VERIFIED. That is the one situation the service forbids and the tool cannot settle: nothing is
// merged, the accounts are reported and none of them is modified. Several accounts carrying the
// same number with at most one of them verified is legitimate (an unverified number proves
// nothing) and is normalised like any other.
type Collision struct {
	Number   string
	UserIDs  []string
	Verified int
}

// Note reports a number carried by several accounts of which exactly one holds it verified. The
// accounts are normalised normally; the line is there so that whoever reads the report knows the
// others carry a number somebody else has proved.
type Note struct {
	Number   string
	UserIDs  []string
	Verified string
}

// ClaimConflict is a verified holder whose number already has a claim in the name of another
// account. The claim is left as it is.
type ClaimConflict struct {
	Number  string
	UserID  string
	OwnerID string
}

// Report is the outcome of one run on one instance.
type Report struct {
	Instance          string
	Applied           bool
	UsersScanned      int
	AlreadyNormalized int
	Changes           []Change
	Changed           int
	Unparseable       []Unparseable
	Collisions        []Collision
	Notes             []Note
	// Claims: the verified holders outside a collision need a claim in the verifiedPhones
	// collection (see userdb.VerifiedPhoneCollection). ClaimsToSeed counts the ones that are
	// missing (dry run); ClaimsSeeded those written (apply); ClaimsPresent those already in the
	// holder's name.
	ClaimsToSeed   int
	ClaimsSeeded   int
	ClaimsPresent  int
	ClaimConflicts []ClaimConflict
	// SkippedConcurrent counts the writes that matched nothing because the stored number had
	// changed since it was read; the next run picks them up.
	SkippedConcurrent int
}

type phoneUser struct {
	ID           primitive.ObjectID `bson:"_id"`
	ContactInfos []struct {
		Type        string `bson:"type"`
		Phone       string `bson:"phone"`
		ConfirmedAt int64  `bson:"confirmedAt"`
	} `bson:"contactInfos"`
	Account struct {
		PhoneVerificationCode struct {
			Phone string `bson:"phone"`
		} `bson:"phoneVerificationCode"`
	} `bson:"account"`
}

// userPlan is what has to be done on one account.
type userPlan struct {
	id          primitive.ObjectID
	contactOld  string
	contactNew  string
	codeOld     string
	codeNew     string
	collides    bool
	contactSeen bool
	// verifiedNumbers are the normalised numbers the account holds confirmed.
	verifiedNumbers []string
}

// NormalizePhoneNumbers brings the stored phone numbers of an instance to E.164 form.
//
// Without apply nothing is written. With apply, each account is updated with a targeted $set
// whose filter names the value that was read, so a number changed in the meantime is not
// overwritten. Accounts that share a normalised number with another account, and numbers that
// cannot be normalised, are reported and never written. Running it again finds nothing left to
// change.
func NormalizePhoneNumbers(svc *userdb.UserDBService, instanceID string, apply bool) (Report, error) {
	report := Report{Instance: instanceID, Applied: apply}

	ctx, cancel := svc.GetContext()
	defer cancel()
	cursor, err := svc.GetCollection(instanceID, userdb.UserCollection).Find(ctx, bson.M{"$or": bson.A{
		bson.M{"contactInfos": bson.M{"$elemMatch": bson.M{"type": models.ContactTypePhone}}},
		bson.M{"account.phoneVerificationCode.phone": bson.M{"$exists": true, "$nin": bson.A{"", nil}}},
	}}, options.Find().SetProjection(bson.M{
		"contactInfos.type": 1, "contactInfos.phone": 1, "contactInfos.confirmedAt": 1,
		"account.phoneVerificationCode.phone": 1,
	}))
	if err != nil {
		return report, err
	}
	var users []phoneUser
	if err := cursor.All(ctx, &users); err != nil {
		return report, err
	}
	report.UsersScanned = len(users)

	// Pass 1: classify every stored number and group the accounts by the number they end up with.
	holders := map[string]map[string]bool{}  // normalised number -> user ids
	verified := map[string]map[string]bool{} // normalised number -> user ids holding it verified
	plans := make([]*userPlan, 0, len(users))
	for _, u := range users {
		plan := &userPlan{id: u.ID}
		plans = append(plans, plan)
		for _, ci := range u.ContactInfos {
			if ci.Type != models.ContactTypePhone || ci.Phone == "" {
				continue
			}
			normalized, err := utils.NormalizePhone(ci.Phone)
			if err != nil {
				report.Unparseable = append(report.Unparseable, Unparseable{u.ID.Hex(), fieldContact, utils.MaskPhone(ci.Phone)})
				continue
			}
			if holders[normalized] == nil {
				holders[normalized] = map[string]bool{}
			}
			if ci.ConfirmedAt > 0 {
				if verified[normalized] == nil {
					verified[normalized] = map[string]bool{}
				}
				verified[normalized][u.ID.Hex()] = true
				plan.verifiedNumbers = append(plan.verifiedNumbers, normalized)
			}
			holders[normalized][u.ID.Hex()] = true
			// A second phone entry on one account is not touched: the service never writes it.
			if plan.contactSeen {
				continue
			}
			plan.contactSeen = true
			plan.contactOld, plan.contactNew = ci.Phone, normalized
		}
		if codePhone := u.Account.PhoneVerificationCode.Phone; codePhone != "" {
			normalized, err := utils.NormalizePhone(codePhone)
			if err != nil {
				report.Unparseable = append(report.Unparseable, Unparseable{u.ID.Hex(), fieldCode, utils.MaskPhone(codePhone)})
			} else {
				plan.codeOld, plan.codeNew = codePhone, normalized
			}
		}
	}

	// Pass 2: a collision is a number that at least two different accounts hold verified. Those
	// accounts are left alone. A number shared with at most one verified holder is not a problem.
	collidingUsers := map[string]bool{}
	collidingNumbers := map[string]bool{}
	for number, ids := range holders {
		if len(verified[number]) >= 2 {
			collidingNumbers[number] = true
			c := Collision{Number: utils.MaskPhone(number), Verified: len(verified[number])}
			for id := range ids {
				c.UserIDs = append(c.UserIDs, id)
				collidingUsers[id] = true
			}
			sort.Strings(c.UserIDs)
			report.Collisions = append(report.Collisions, c)
			continue
		}
		if len(ids) >= 2 && len(verified[number]) == 1 {
			n := Note{Number: utils.MaskPhone(number)}
			for id := range ids {
				n.UserIDs = append(n.UserIDs, id)
			}
			for id := range verified[number] {
				n.Verified = id
			}
			sort.Strings(n.UserIDs)
			report.Notes = append(report.Notes, n)
		}
	}
	sort.Slice(report.Collisions, func(i, j int) bool { return report.Collisions[i].UserIDs[0] < report.Collisions[j].UserIDs[0] })
	sort.Slice(report.Notes, func(i, j int) bool { return report.Notes[i].UserIDs[0] < report.Notes[j].UserIDs[0] })

	// Pass 3: report and, when asked, write the rest.
	for _, plan := range plans {
		if collidingUsers[plan.id.Hex()] {
			plan.collides = true
			continue
		}
		contactChange := plan.contactSeen && plan.contactOld != plan.contactNew
		codeChange := plan.codeOld != "" && plan.codeOld != plan.codeNew
		if plan.contactSeen && !contactChange {
			report.AlreadyNormalized++
		}
		if contactChange {
			report.Changes = append(report.Changes, newChange(plan.id.Hex(), fieldContact, plan.contactOld, plan.contactNew))
		}
		if codeChange {
			report.Changes = append(report.Changes, newChange(plan.id.Hex(), fieldCode, plan.codeOld, plan.codeNew))
		}
		if !apply {
			continue
		}
		if !contactChange && !codeChange {
			continue
		}
		matched, err := writePlan(svc, instanceID, plan, contactChange, codeChange)
		if err != nil {
			return report, err
		}
		if matched {
			report.Changed++
		} else {
			report.SkippedConcurrent++
		}
	}
	sort.Slice(report.Changes, func(i, j int) bool {
		if report.Changes[i].UserID != report.Changes[j].UserID {
			return report.Changes[i].UserID < report.Changes[j].UserID
		}
		return report.Changes[i].Field < report.Changes[j].Field
	})

	// Pass 4: the verified holders outside a collision get their claim.
	owners := map[string]string{} // normalised number -> its only verified holder
	for number, ids := range verified {
		if collidingNumbers[number] {
			continue
		}
		for id := range ids {
			owners[number] = id
		}
	}
	if err := seedClaims(svc, instanceID, owners, apply, &report); err != nil {
		return report, err
	}
	return report, nil
}

// newChange builds the masked report line of one number.
func newChange(userID, field, old, new string) Change {
	c := Change{
		UserID: userID, Field: field, Old: utils.MaskPhone(old), New: utils.MaskPhone(new),
		OldDigits: countDigits(old), NewDigits: countDigits(new),
	}
	// A leading "00" is the international prefix, not part of the number.
	oldDigits := c.OldDigits
	if strings.HasPrefix(strings.TrimSpace(old), "00") {
		oldDigits -= 2
	}
	c.TrunkZeroRemoved = oldDigits > c.NewDigits
	return c
}

func countDigits(s string) int {
	n := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n++
		}
	}
	return n
}

// seedClaims makes sure every verified holder in owners has the claim on its number in the
// verifiedPhones collection, keyed by the E.164 number: absent claims are inserted, a claim
// already in the holder's name is left, and a claim in the name of another account is reported
// and left as it is. The insert is the same one the service uses, so running it next to a live
// service, or twice, is safe. Without apply it only counts.
func seedClaims(svc *userdb.UserDBService, instanceID string, owners map[string]string, apply bool, report *Report) error {
	claims := svc.GetCollection(instanceID, userdb.VerifiedPhoneCollection)
	numbers := make([]string, 0, len(owners))
	for number := range owners {
		numbers = append(numbers, number)
	}
	sort.Strings(numbers)

	for _, number := range numbers {
		ownerHex := owners[number]
		ownerID, err := primitive.ObjectIDFromHex(ownerHex)
		if err != nil {
			return err
		}

		if apply {
			ctx, cancel := svc.GetContext()
			_, err = claims.InsertOne(ctx, bson.M{"_id": number, "userId": ownerID, "claimedAt": time.Now().Unix(), "rev": 0})
			cancel()
			if err == nil {
				report.ClaimsSeeded++
				continue
			}
			if !mongo.IsDuplicateKeyError(err) {
				return err
			}
		}

		var current struct {
			UserID primitive.ObjectID `bson:"userId"`
		}
		ctx, cancel := svc.GetContext()
		err = claims.FindOne(ctx, bson.M{"_id": number}).Decode(&current)
		cancel()
		switch {
		case errors.Is(err, mongo.ErrNoDocuments):
			// Dry run: nothing there yet. (With apply it was removed between the insert and
			// the read; the next run picks it up.)
			if !apply {
				report.ClaimsToSeed++
			}
		case err != nil:
			return err
		case current.UserID == ownerID:
			report.ClaimsPresent++
		default:
			report.ClaimConflicts = append(report.ClaimConflicts, ClaimConflict{
				Number: utils.MaskPhone(number), UserID: ownerHex, OwnerID: current.UserID.Hex(),
			})
		}
	}
	return nil
}

// writePlan applies one account's change. The filter always names the values read earlier. When
// the contact and the code carry the same number they change in one update, so the account is
// never seen with a code bound to a number its contact no longer has.
func writePlan(svc *userdb.UserDBService, instanceID string, plan *userPlan, contactChange, codeChange bool) (bool, error) {
	ctx, cancel := svc.GetContext()
	defer cancel()
	users := svc.GetCollection(instanceID, userdb.UserCollection)

	contactMatch := bson.M{"$elemMatch": bson.M{"type": models.ContactTypePhone, "phone": plan.contactOld}}
	filter := bson.M{"_id": plan.id}

	switch {
	case contactChange && codeChange && plan.codeOld == plan.contactOld:
		filter["contactInfos"] = contactMatch
		filter["account.phoneVerificationCode.phone"] = plan.codeOld
		res, err := users.UpdateOne(ctx, filter, mongo.Pipeline{bson.D{{Key: "$set", Value: bson.M{
			"contactInfos": bson.M{"$map": bson.M{
				"input": "$contactInfos", "as": "c",
				"in": bson.M{"$cond": bson.A{
					bson.M{"$and": bson.A{
						bson.M{"$eq": bson.A{"$$c.type", models.ContactTypePhone}},
						bson.M{"$eq": bson.A{"$$c.phone", plan.contactOld}},
					}},
					bson.M{"$mergeObjects": bson.A{"$$c", bson.M{"phone": plan.contactNew}}},
					"$$c",
				}},
			}},
			"account.phoneVerificationCode.phone": plan.codeNew,
		}}}})
		return err == nil && res.MatchedCount > 0, err
	case contactChange && codeChange:
		// Two different numbers: two independent updates, each guarded by its own value.
		a, err := writePlan(svc, instanceID, plan, true, false)
		if err != nil {
			return a, err
		}
		b, err := writePlan(svc, instanceID, plan, false, true)
		return a || b, err
	case contactChange:
		filter["contactInfos"] = contactMatch
		res, err := users.UpdateOne(ctx, filter,
			bson.M{"$set": bson.M{"contactInfos.$[ci].phone": plan.contactNew}},
			options.Update().SetArrayFilters(options.ArrayFilters{Filters: []interface{}{
				bson.M{"ci.type": models.ContactTypePhone, "ci.phone": plan.contactOld},
			}}))
		return err == nil && res.MatchedCount > 0, err
	default:
		filter["account.phoneVerificationCode.phone"] = plan.codeOld
		res, err := users.UpdateOne(ctx, filter,
			bson.M{"$set": bson.M{"account.phoneVerificationCode.phone": plan.codeNew}})
		return err == nil && res.MatchedCount > 0, err
	}
}
