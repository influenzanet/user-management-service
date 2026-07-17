package clients

import (
	"errors"
	"testing"
)

func TestParseSendError(t *testing.T) {
	t.Run("with recipient not in allowed list error", func(t *testing.T) {
		respObj := map[string]any{
			"error": map[string]any{
				"message": "(#131030) Recipient phone number not in allowed list",
				"type":    "OAuthException",
				"code":    float64(131030),
			},
		}
		err := parseSendError(400, respObj)
		if !errors.Is(err, ErrRecipientNotAllowed) {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("with another error code", func(t *testing.T) {
		respObj := map[string]any{
			"error": map[string]any{
				"message": "(#131026) Message undeliverable",
				"code":    float64(131026),
			},
		}
		err := parseSendError(400, respObj)
		if errors.Is(err, ErrRecipientNotAllowed) {
			t.Error("should not detect recipient not allowed")
		}
		if err == nil {
			t.Error("should return an error")
		}
	})

	t.Run("with empty response body", func(t *testing.T) {
		err := parseSendError(500, map[string]any{})
		if err == nil || errors.Is(err, ErrRecipientNotAllowed) {
			t.Errorf("unexpected error: %v", err)
		}
	})
}
