package main

import (
	"crypto/rand"
	b64 "encoding/base64"
	"fmt"

	"github.com/coneno/logger"
)

func main() {
	keyLength := 32 // bytes

	secret := make([]byte, keyLength)

	_, err := rand.Read(secret)
	if err != nil {
		logger.Error.Fatal(err)
	}
	secretStr := b64.StdEncoding.EncodeToString(secret)
	fmt.Println(secretStr)
}
