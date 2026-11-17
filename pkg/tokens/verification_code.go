package tokens

import (
	"crypto/rand"
	"math/big"
)

const codeCharSet = "1234567890"

// GenerateVerificationCode returns length digits drawn uniformly at random. Each digit is drawn
// with rand.Int, which rejects the values a plain byte modulo 10 would over-represent.
func GenerateVerificationCode(length int) (string, error) {
	buffer := make([]byte, length)
	charsetLength := big.NewInt(int64(len(codeCharSet)))
	for i := 0; i < length; i++ {
		n, err := rand.Int(rand.Reader, charsetLength)
		if err != nil {
			return "", err
		}
		buffer[i] = codeCharSet[n.Int64()]
	}
	return string(buffer), nil
}
