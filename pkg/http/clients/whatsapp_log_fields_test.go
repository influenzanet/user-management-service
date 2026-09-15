package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coneno/logger"
)

// A Meta error body as returned by the Cloud API: the numeric fields are safe to log, the
// free text ("message", "details") is not.
const metaErrorBody = `{"error":{"message":"(#131030) Recipient phone number not in allowed list","type":"OAuthException","code":131030,"error_data":{"messaging_product":"whatsapp","details":"Numero di telefono del destinatario non presente nella lista dei numeri consentiti"},"error_subcode":2494010,"fbtrace_id":"AbCdEf123"}}`

func TestMetaErrorFieldsKeepOnlyTheNumericFieldsAndTheTrace(t *testing.T) {
	var respObj map[string]any
	if err := json.Unmarshal([]byte(metaErrorBody), &respObj); err != nil {
		t.Fatal(err)
	}
	got := metaErrorFields(respObj)
	for _, want := range []string{"code=131030", "subcode=2494010", "fbtrace_id=AbCdEf123"} {
		if !strings.Contains(got, want) {
			t.Errorf("metaErrorFields() = %q, want it to contain %q", got, want)
		}
	}
	for _, forbidden := range []string{"Recipient phone number", "Numero di telefono", "OAuthException"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("metaErrorFields() = %q, must not contain Meta's free text %q", got, forbidden)
		}
	}
	if got := metaErrorFields(nil); got == "" {
		t.Errorf("metaErrorFields(nil) must still describe the failure, got empty string")
	}
}

func TestFailedSendsLogNoMetaFreeText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(metaErrorBody))
	}))
	t.Cleanup(server.Close)

	var buf bytes.Buffer
	previous := logger.Error.Writer()
	logger.Error.SetOutput(&buf)
	t.Cleanup(func() { logger.Error.SetOutput(previous) })

	c := NewWhatsAppClient("token", "phone-id", "verify_code", "")
	c.apiBaseURL = server.URL
	ctx := context.Background()
	_ = c.SendVerificationCode(ctx, "+391234567890", "123456", "it")
	_ = c.SendTextMessage(ctx, "+391234567890", "hello")
	_ = c.SendTemplateMessage(ctx, "+391234567890", "weekly", "it", map[string]string{})

	logged := buf.String()
	if strings.Count(logged, "status=400") != 3 {
		t.Fatalf("expected three failure lines with the HTTP status, got:\n%s", logged)
	}
	if strings.Count(logged, "code=131030") != 3 || strings.Count(logged, "fbtrace_id=AbCdEf123") != 3 {
		t.Errorf("every failure line must carry Meta's code and trace id, got:\n%s", logged)
	}
	for _, forbidden := range []string{"Recipient phone number", "Numero di telefono", "map["} {
		if strings.Contains(logged, forbidden) {
			t.Errorf("failure log must not contain Meta's free text or the raw body (%q), got:\n%s", forbidden, logged)
		}
	}
}
