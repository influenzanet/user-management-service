// Package whatsappqueue withdraws a participant's queued WhatsApp messages from messaging-service
// when the participant withdraws what they were queued for: the account, the phone number,
// the whatsapp channel, or a subscription.
package whatsappqueue

import (
	"context"
	"sync"
	"time"

	"github.com/coneno/logger"
	messageAPI "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Defaults: three attempts, one and then two seconds apart, each bounded by a timeout.
var (
	DefaultBackoff        = []time.Duration{1 * time.Second, 2 * time.Second}
	DefaultAttemptTimeout = 5 * time.Second
)

// Canceller calls messaging's DeleteOutgoingWhatsAppForUser with bounded retries. The participant's
// action never depends on it: when messaging stays unreachable it logs `WA_ALERT cancel_failed`
// with the user ID, and the queued rows are sent or expire at the queue's maximum age. The
// RPC is idempotent, so a retry after a lost answer is harmless.
type Canceller struct {
	client messageAPI.MessagingServiceApiClient
	// Backoff lists the waits between attempts: there is one attempt more than waits.
	Backoff []time.Duration
	// AttemptTimeout bounds each call.
	AttemptTimeout time.Duration

	pending sync.WaitGroup
}

// NewCanceller returns a Canceller over the messaging client, with the default retry policy.
func NewCanceller(client messageAPI.MessagingServiceApiClient) *Canceller {
	return &Canceller{client: client, Backoff: DefaultBackoff, AttemptTimeout: DefaultAttemptTimeout}
}

// Cancel withdraws the user's queued WhatsApp messages: of the given types, or of every type when
// messageTypes is empty. purgeArchive is for account deletion only: it also deletes the user's
// archived messages, and requires every type (messaging refuses it with a type filter). It
// returns whether messaging confirmed the cancellation.
func (c *Canceller) Cancel(instanceID, userID string, messageTypes []string, purgeArchive bool) bool {
	if c == nil || c.client == nil {
		logger.Error.Printf("WA_ALERT cancel_failed user=%s: no messaging client", userID)
		return false
	}
	req := &messageAPI.DeleteOutgoingWhatsAppForUserReq{
		InstanceId:   instanceID,
		UserId:       userID,
		MessageTypes: messageTypes,
		PurgeArchive: purgeArchive,
	}
	var err error
	attempts := 0
	for attempts < len(c.Backoff)+1 {
		if attempts > 0 {
			time.Sleep(c.Backoff[attempts-1])
		}
		attempts++
		ctx, cancel := context.WithTimeout(context.Background(), c.AttemptTimeout)
		var resp *messageAPI.DeleteOutgoingWhatsAppForUserResp
		resp, err = c.client.DeleteOutgoingWhatsAppForUser(ctx, req)
		cancel()
		if err == nil {
			if resp.GetRemovedFromQueue() > 0 || resp.GetRemovedFromArchive() > 0 {
				logger.Info.Printf("whatsapp queue: user=%s removed_from_queue=%d removed_from_archive=%d",
					userID, resp.GetRemovedFromQueue(), resp.GetRemovedFromArchive())
			}
			return true
		}
		if !retryable(err) {
			break
		}
	}
	// The error message from messaging is left out: only the user ID and the status code.
	logger.Error.Printf("WA_ALERT cancel_failed user=%s attempts=%d code=%s", userID, attempts, status.Code(err))
	return false
}

// CancelInBackground runs Cancel in its own goroutine, so that the participant's request does not
// wait through the retries.
func (c *Canceller) CancelInBackground(instanceID, userID string, messageTypes []string, purgeArchive bool) {
	if c == nil {
		c.Cancel(instanceID, userID, messageTypes, purgeArchive) // logs the alert
		return
	}
	c.pending.Add(1)
	go func() {
		defer c.pending.Done()
		c.Cancel(instanceID, userID, messageTypes, purgeArchive)
	}()
}

// Wait returns once every cancellation started in the background has finished.
func (c *Canceller) Wait() {
	if c != nil {
		c.pending.Wait()
	}
}

// retryable tells a failure that another attempt may fix (messaging down, slow or overloaded)
// from one it cannot (a request messaging refuses, or a messaging without the RPC).
func retryable(err error) bool {
	switch status.Code(err) {
	case codes.InvalidArgument, codes.Unimplemented, codes.PermissionDenied,
		codes.Unauthenticated, codes.FailedPrecondition, codes.NotFound, codes.OutOfRange:
		return false
	}
	return true
}
