package clients

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coneno/logger"
)

// Meta's id for a message to +391234567890: the number is inside it, base64-encoded.
const sampleWAMID = "wamid.HBgLMzkxMjM0NTY3ODkwFQIAERgSQUJDREVGMDEyMzQ1Njc4OQA="

// The reference is pinned: messaging-service logs the delivery statuses of the verification
// codes under it, and the two services' logs only match while both give the same reference.
// Fails if the hash, its length or its encoding changes here without the same change in
// messaging-service's types.WAMIDLogRef, which pins the same value.
func TestWAMIDLogRefIsTheSameInEveryService(t *testing.T) {
	if got := wamidLogRef(sampleWAMID); got != "a2205e2a010d" {
		t.Fatalf("wamidLogRef = %q, want a2205e2a010d (messaging-service pins the same value)", got)
	}
	if got := wamidLogRef(""); got != "none" {
		t.Errorf("empty id: %q, want none", got)
	}
}

// sendCode sends one verification code to a server that accepts it with body, and returns
// what was logged.
func sendCode(t *testing.T, body string) string {
	t.Helper()
	var logged bytes.Buffer
	for _, l := range []*log.Logger{logger.Info, logger.Warning, logger.Error} {
		prev := l.Writer()
		l.SetOutput(&logged)
		t.Cleanup(func() { l.SetOutput(prev) })
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	c := NewWhatsAppClient("token", "phone-id", "verify_code", "")
	c.apiBaseURL = server.URL
	if err := c.SendVerificationCode(context.Background(), "+391234567890", "123456", "it"); err != nil {
		t.Fatalf("an accepted send returned an error: %v", err)
	}
	return logged.String()
}

// An accepted verification code logs the reference of its id, so that the statuses
// messaging-service logs for it can be told apart from those of the scheduler's messages, and
// never the id itself, which holds the recipient's number.
// Fails if the reference is missing or wrong, or if the raw id or the number reaches the log.
func TestSendVerificationCodeLogsTheIDReferenceOnly(t *testing.T) {
	logs := sendCode(t, `{"messaging_product":"whatsapp",`+
		`"contacts":[{"input":"+391234567890","wa_id":"391234567890"}],`+
		`"messages":[{"id":"`+sampleWAMID+`","message_status":"accepted"}]}`)
	if !strings.Contains(logs, "delivered to API, wamid_ref=a2205e2a010d") {
		t.Errorf("the send log does not carry the id reference: %q", logs)
	}
	for _, leaked := range []string{sampleWAMID, "HBgLMzkxMjM0NTY3ODkw", "1234567890"} {
		if strings.Contains(logs, leaked) {
			t.Errorf("the send log holds %q: %q", leaked, logs)
		}
	}
}

// An accepted send whose response names no readable id is still a success, logged with no
// reference, and the response, which echoes the number, is not logged.
// Fails if such a send is reported as failed, if a reference is made up, or if a response is
// read without bound.
func TestSendVerificationCodeWithoutAnID(t *testing.T) {
	echo := `"contacts":[{"input":"+391234567890","wa_id":"391234567890"}]`
	for name, body := range map[string]string{
		"empty body":        ``,
		"not JSON":          `OK`,
		"no messages":       `{` + echo + `}`,
		"empty id":          `{` + echo + `,"messages":[{"id":""}]}`,
		"blank id":          `{` + echo + `,"messages":[{"id":"  "}]}`,
		"id past the limit": `{"padding":"` + strings.Repeat("x", maxAcceptedResponseBytes) + `",` + echo + `,"messages":[{"id":"wamid.A"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			logs := sendCode(t, body)
			if !strings.Contains(logs, "delivered to API, wamid_ref=none") {
				t.Errorf("log %q, want wamid_ref=none", logs)
			}
			if strings.Contains(logs, "1234567890") {
				t.Errorf("the log holds the recipient's full number: %q", logs)
			}
		})
	}
}
