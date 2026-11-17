package userdb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/influenzanet/user-management-service/pkg/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// PhoneVerificationSendCollection holds one document per destination phone number, recording the
// verification messages recently sent to it. It is the counterpart of the per-account budget kept
// on the user document: that one bounds what a single account can spend, this one bounds what a
// single number can receive, whoever asked for it.
const PhoneVerificationSendCollection = "phoneVerificationSends"

// PhoneVerificationSendRetention is how long a destination's record is kept, in seconds, and is
// the TTL of the index below. It is also the longest window a reservation may count over:
// ReservePhoneDestinationSendSlot refuses a longer one, so a record is never swept away while it
// still holds a send that counts. A day covers the daily cap, the longest of the limits.
const PhoneVerificationSendRetention = 24 * 60 * 60

// SendLimit is one ceiling on the verification messages a destination number may receive: at most
// MaxSends in the last WindowSeconds.
type SendLimit struct {
	MaxSends      int
	WindowSeconds int64
}

// ErrInvalidSendLimits reports a reservation asked with no limit, a non-positive window, or a
// window longer than the records are kept. The send must be refused: a budget that cannot be
// counted over its whole window has not been checked.
var ErrInvalidSendLimits = errors.New("invalid send limits for the destination number")

// reservationRetries bounds the duplicate key retry below. One retry is enough in principle: the
// second attempt runs against a document that certainly exists. The second is there so a single
// unlucky interleaving cannot turn into a refusal.
const reservationRetries = 3

// ErrPhoneDestinationReservationFailed reports that the destination budget could not be settled
// within the allowed attempts. Callers must treat it as they treat any other error here, by
// refusing the send: a budget that cannot be checked has not been checked.
var ErrPhoneDestinationReservationFailed = errors.New("could not reserve a send slot for the destination number")

// collectionRefPhoneVerificationSends gets the per-destination send records of an instance.
func (dbService *UserDBService) collectionRefPhoneVerificationSends(instanceID string) *mongo.Collection {
	return dbService.DBClient.Database(dbService.DBNamePrefix + instanceID + "_users").Collection(PhoneVerificationSendCollection)
}

// phoneVerificationSendKey derives the storage key of a destination number.
//
// The number itself is never written: these records say that somebody asked for a code to be sent
// to a given number, which is participant data of the same kind as the contact list, held in a
// collection that exists only for rate limiting and is read by nobody. A hash is enough to count
// against, and it keeps the collection from being a second, unguarded copy of the contact list.
//
// The digest is an unsalted SHA-256, because this service has no server secret in its
// configuration to key an HMAC with: JWT_TOKEN_KEY is read inside the token package and is a
// signing key, which is the wrong thing to reuse as a lookup key. Unsalted means the key space
// (phone numbers) is small enough to enumerate for anyone who already holds the collection, so
// this hides the numbers from casual reading and from anything that ends up in a dump or a log,
// not from an attacker with database access. Giving the service a dedicated pepper would close
// that gap and is the natural follow-up.
//
// The number is canonicalised first so that no spelling of it buys a second budget: it is
// normalised to E.164 (see utils.NormalizePhone) and the leading "+" is dropped, so "+0039...",
// "0039..." and "+39..." are the same telephone. A value that cannot be normalised is reduced to
// its digits without zero padding.
func phoneVerificationSendKey(phone string) string {
	canonical, err := utils.NormalizePhone(phone)
	if err == nil {
		canonical = canonical[1:]
	} else {
		// Not a number that can be normalised: keep the digits, without zero padding.
		canonical = strings.TrimLeft(strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, phone), "0")
	}
	digest := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(digest[:])
}

// ReservePhoneDestinationSendSlot atomically consumes one send slot of the budget a phone number
// has as a destination, and reports whether there was one. The slot is granted only when every
// limit has room: with an hourly and a daily limit, a number that took 10 codes this hour or 20
// today gets no more.
//
// It is shaped like ReservePhoneVerificationSlot, and for the same reason: the check and the
// increment are a single find-and-modify, so concurrent requests, including ones on other
// replicas, cannot both pass the check on the same free slot. All limits are checked in that one
// update, against one array: a separate reservation per limit would not work, because each would
// prune the array to its own window and erase what the longer one counts. The array keeps the
// sends of the longest window, which prunes the older entries on every reservation and bounds the
// array by that window's limit. Whether the slot was granted is read from the document as it was
// before the update: the same window starts are used on both sides, so the counts the pipeline
// decided on and the counts read here are the same counts.
//
// The budget is keyed by destination rather than by account because accounts are free to create.
// Without this, rotating accounts buys an unbounded number of messages to one victim's number, at
// the platform's expense. Callers reserve here only once a message is really about to be sent:
// charging a refusal that delivers nothing would let anyone spend a stranger's window and lock
// them out of their own verification.
//
// A refusal is reported to the caller with the same text as the per-account one, so that the
// answer does not say whether the number belongs to somebody else.
//
// An error means the budget could not be established, and every caller must refuse the send on
// it: this check is only worth having if it fails closed.
func (dbService *UserDBService) ReservePhoneDestinationSendSlot(instanceID string, phone string, limits []SendLimit) (bool, error) {
	if len(limits) == 0 {
		return false, ErrInvalidSendLimits
	}
	longestWindow := int64(0)
	for _, limit := range limits {
		if limit.WindowSeconds <= 0 || limit.WindowSeconds > PhoneVerificationSendRetention {
			return false, ErrInvalidSendLimits
		}
		if limit.WindowSeconds > longestWindow {
			longestWindow = limit.WindowSeconds
		}
	}

	now := time.Now().Unix()

	sendsSince := func(windowStart int64) bson.M {
		return bson.M{"$filter": bson.M{
			"input": bson.M{"$ifNull": bson.A{"$sends", bson.A{}}},
			"as":    "send",
			"cond":  bson.M{"$gt": bson.A{"$$send", windowStart}},
		}}
	}
	kept := sendsSince(now - longestWindow)
	roomInEveryLimit := bson.A{}
	for _, limit := range limits {
		roomInEveryLimit = append(roomInEveryLimit,
			bson.M{"$lt": bson.A{bson.M{"$size": sendsSince(now - limit.WindowSeconds)}, limit.MaxSends}})
	}

	// Every expression in a $set stage reads the document as it was before the stage, so the
	// condition and the array it guards see the same pre-update sends.
	pipeline := mongo.Pipeline{bson.D{{Key: "$set", Value: bson.M{
		"sends": bson.M{"$cond": bson.M{
			"if":   bson.M{"$and": roomInEveryLimit},
			"then": bson.M{"$concatArrays": bson.A{kept, bson.A{now}}},
			"else": kept,
		}},
		// Stamped on every attempt, granted or refused, which is what the TTL expires on. A
		// refused attempt only happens while the record is full, and every send it holds is
		// older than the attempt, so they all leave the window before the record does.
		"updatedAt": primitive.NewDateTimeFromTime(time.Unix(now, 0)),
	}}}}
	filter := bson.M{"_id": phoneVerificationSendKey(phone)}
	opts := options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.Before)

	for attempt := 0; attempt < reservationRetries; attempt++ {
		granted, err := dbService.reservePhoneDestinationSendSlotOnce(instanceID, filter, pipeline, opts, limits, now)
		if err == nil {
			return granted, nil
		}
		// Two requests can race to create the record of a number nobody has written to yet. The
		// loser is told the key is taken, which is not a refusal: the record now exists, so the
		// same reservation is attempted again against it.
		if mongo.IsDuplicateKeyError(err) {
			continue
		}
		return false, err
	}
	return false, ErrPhoneDestinationReservationFailed
}

// reservePhoneDestinationSendSlotOnce runs one reservation attempt and reports whether the slot
// was granted, reading the decision off the pre-update document.
func (dbService *UserDBService) reservePhoneDestinationSendSlotOnce(
	instanceID string,
	filter bson.M,
	pipeline mongo.Pipeline,
	opts *options.FindOneAndUpdateOptions,
	limits []SendLimit,
	now int64,
) (bool, error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	var before struct {
		Sends []int64 `bson:"sends"`
	}
	err := dbService.collectionRefPhoneVerificationSends(instanceID).
		FindOneAndUpdate(ctx, filter, pipeline, opts).Decode(&before)
	if errors.Is(err, mongo.ErrNoDocuments) {
		// No record existed, so the upsert created one holding this send: nothing had been sent
		// to this number in any window and the slot is granted.
		return true, nil
	}
	if err != nil {
		return false, err
	}

	for _, limit := range limits {
		windowStart := now - limit.WindowSeconds
		used := 0
		for _, sentAt := range before.Sends {
			if sentAt > windowStart {
				used++
			}
		}
		if used >= limit.MaxSends {
			return false, nil
		}
	}
	return true, nil
}

// PhoneDestinationSendsInWindow reports how many verification messages were sent to a number
// inside the given window. It reads the record ReservePhoneDestinationSendSlot writes and spends
// nothing, so it answers "has this number been charged?" without charging it.
func (dbService *UserDBService) PhoneDestinationSendsInWindow(instanceID string, phone string, windowSeconds int64) (int, error) {
	ctx, cancel := dbService.getContext()
	defer cancel()

	var record struct {
		Sends []int64 `bson:"sends"`
	}
	err := dbService.collectionRefPhoneVerificationSends(instanceID).FindOne(
		ctx, bson.M{"_id": phoneVerificationSendKey(phone)},
	).Decode(&record)
	if errors.Is(err, mongo.ErrNoDocuments) {
		// Nothing was ever sent to this number, which is not an error.
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	windowStart := time.Now().Unix() - windowSeconds
	sent := 0
	for _, sentAt := range record.Sends {
		if sentAt > windowStart {
			sent++
		}
	}
	return sent, nil
}

// CreateIndexForPhoneVerificationSends makes sure the TTL index that keeps the send records from
// growing without bound exists and expires them after PhoneVerificationSendRetention. It is
// separate from CreateIndexForUser because it indexes a different collection; both are called
// once per instance at startup.
//
// Creating an index whose key already has one with another TTL fails rather than changing it, so
// an index left by an earlier version with a shorter retention is changed in place with collMod.
// Left as it was, it would delete a record while its sends still count towards the daily limit.
func (dbService *UserDBService) CreateIndexForPhoneVerificationSends(instanceID string) error {
	ctx, cancel := dbService.getContext()
	defer cancel()

	collection := dbService.collectionRefPhoneVerificationSends(instanceID)
	expireAfter, found, err := phoneVerificationSendTTL(ctx, collection)
	if err != nil {
		return err
	}
	if !found {
		_, err = collection.Indexes().CreateOne(
			ctx, mongo.IndexModel{
				Keys:    bson.D{{Key: "updatedAt", Value: 1}},
				Options: options.Index().SetExpireAfterSeconds(int32(PhoneVerificationSendRetention)),
			},
		)
		return err
	}
	if expireAfter == PhoneVerificationSendRetention {
		return nil
	}
	return collection.Database().RunCommand(ctx, bson.D{
		{Key: "collMod", Value: PhoneVerificationSendCollection},
		{Key: "index", Value: bson.D{
			{Key: "keyPattern", Value: bson.D{{Key: "updatedAt", Value: 1}}},
			{Key: "expireAfterSeconds", Value: int64(PhoneVerificationSendRetention)},
		}},
	}).Err()
}

// phoneVerificationSendTTL looks for the index on updatedAt alone and returns its TTL in seconds
// (-1 if it has none).
func phoneVerificationSendTTL(ctx context.Context, collection *mongo.Collection) (int64, bool, error) {
	// The driver lists no index, without an error, for a collection that does not exist yet.
	cursor, err := collection.Indexes().List(ctx)
	if err != nil {
		return 0, false, err
	}
	var specs []bson.M
	if err := cursor.All(ctx, &specs); err != nil {
		return 0, false, err
	}
	for _, spec := range specs {
		key, ok := spec["key"].(bson.M)
		if !ok || len(key) != 1 {
			continue
		}
		if _, onUpdatedAt := key["updatedAt"]; !onUpdatedAt {
			continue
		}
		switch expire := spec["expireAfterSeconds"].(type) {
		case int32:
			return int64(expire), true, nil
		case int64:
			return expire, true, nil
		case float64:
			return int64(expire), true, nil
		default:
			return -1, true, nil
		}
	}
	return 0, false, nil
}
