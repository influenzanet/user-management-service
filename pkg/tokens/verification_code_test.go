package tokens

import (
	"testing"

	"github.com/coneno/logger"
)

func TestGenerateVerificationCode(t *testing.T) {
	t.Run("with 4 digits", func(t *testing.T) {
		code, err := GenerateVerificationCode(4)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if len(code) != 4 {
			t.Errorf("unexpected length: %d", len(code))
			logger.Error.Println(code)
		}
	})

	t.Run("with 6 digits", func(t *testing.T) {
		code, err := GenerateVerificationCode(6)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if len(code) != 6 {
			t.Errorf("unexpected length: %d", len(code))
		}
	})
}
