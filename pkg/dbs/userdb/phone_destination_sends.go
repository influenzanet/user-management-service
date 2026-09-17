package userdb

import (
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
// the TTL of the index below. A record stops meaning anything once every timestamp in it has left
// the rate limit window, and the window is never longer than this, so nothing that still counts is
// ever swept away. Keeping it exactly this long also keeps the collection bounded by the numbers
// contacted in the last hour rather than by every number ever contacted.
const PhoneVerificationSendRetention = 60 * 60

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
// The number is canonicalised first so that no spelling of it buys a second budget: the
// separators every write path already strips, then the leading "+" and any zero padding of the
// international prefix, since a country code never starts with a zero and "+0039...", "0039..."
// and "+39..." are the same telephone.
func phoneVerificationSendKey(phone string) string {
	canonical := strings.TrimLeft(strings.TrimPrefix(utils.SanitizePhone(phone), "+"), "0")
	digest := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(digest[:])
}

// ReservePhoneDestinationSendSlot atomically consumes one send slot of the budget a phone number
// has as a destination, and reports whether there was one.
//
// It is shaped like ReservePhoneVerificationSlot, and for the same reason: the check and the
// increment are a single find-and-modify, so concurrent requests, including ones on other
// replicas, cannot both pass the check on the same free slot. The pipeline appends the new send
// to the in-window subset of the recorded ones, which prunes the expired entries on every
// reservation and bounds the array by a window's worth of sends. Whether the slot was granted is
// read from the document as it was before the update: the same window start is used on both sides,
// so the count the pipeline decided on and the count read here are the same count.
//
// The budget is keyed by destination rather than by account because accounts are free to create.
// Without this, rotating accounts buys an unbounded number of messages to one victim's number, at
// the platform's expense. Callers reserve here only once a message is really about to be sent:
// charging a refusal that delivers nothing, such as the uniqueness probe, would let anyone spend
// a stranger's window and lock them out of their own verification.
//
// A refusal is reported to the caller with the same text as the per-account one, so that the
// answer does not say whether the number belongs to somebody else.
//
// An error means the budget could not be established, and every caller must refuse the send on
// it: this check is only worth having if it fails closed.
func (dbService *UserDBService) ReservePhoneDestinationSendSlot(instanceID string, phone string, maxSends int, windowSeconds int64) (bool, error) {
	now := time.Now().Unix()
	windowStart := now - windowSeconds

	sendsInWindow := bson.M{"$filter": bson.M{
		"input": bson.M{"$ifNull": bson.A{"$sends", bson.A{}}},
		"as":    "send",
		"cond":  bson.M{"$gt": bson.A{"$$send", windowStart}},
	}}
	// Every expression in a $set stage reads the document as it was before the stage, so the
	// condition and the array it guards see the same pre-update sends.
	pipeline := mongo.Pipeline{bson.D{{Key: "$set", Value: bson.M{
		"sends": bson.M{"$cond": bson.M{
			"if":   bson.M{"$lt": bson.A{bson.M{"$size": sendsInWindow}, maxSends}},
			"then": bson.M{"$concatArrays": bson.A{sendsInWindow, bson.A{now}}},
			"else": sendsInWindow,
		}},
		// Stamped on every attempt, granted or refused, which is what the TTL expires on. A
		// refused attempt only happens while the record is full, and every send it holds is
		// older than the attempt, so they all leave the window before the record does.
		"updatedAt": primitive.NewDateTimeFromTime(time.Unix(now, 0)),
	}}}}
	filter := bson.M{"_id": phoneVerificationSendKey(phone)}
	opts := options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.Before)

	for attempt := 0; attempt < reservationRetries; attempt++ {
		granted, err := dbService.reservePhoneDestinationSendSlotOnce(instanceID, filter, pipeline, opts, maxSends, windowStart)
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
	maxSends int,
	windowStart int64,
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
		// to this number in the window and the slot is granted.
		return true, nil
	}
	if err != nil {
		return false, err
	}

	used := 0
	for _, sentAt := range before.Sends {
		if sentAt > windowStart {
			used++
		}
	}
	return used < maxSends, nil
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

// CreateIndexForPhoneVerificationSends creates the TTL index that keeps the send records from
// growing without bound. It is separate from CreateIndexForUser because it indexes a different
// collection; both are called once per instance at startup.
func (dbService *UserDBService) CreateIndexForPhoneVerificationSends(instanceID string) error {
	ctx, cancel := dbService.getContext()
	defer cancel()

	_, err := dbService.collectionRefPhoneVerificationSends(instanceID).Indexes().CreateOne(
		ctx, mongo.IndexModel{
			Keys:    bson.D{{Key: "updatedAt", Value: 1}},
			Options: options.Index().SetExpireAfterSeconds(int32(PhoneVerificationSendRetention)),
		},
	)
	return err
}
