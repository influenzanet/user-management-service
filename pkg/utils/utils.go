package utils

import (
	"fmt"
	"math/rand"
	"net/mail"
	"regexp"
	"strings"

	"github.com/influenzanet/go-utils/pkg/api_types"
)

func SanitizeEmail(email string) string {
	email = strings.ToLower(email)
	email = strings.Trim(email, " \n\r")
	return email
}

// CheckEmailFormat to check if input string is a correct email address
func CheckEmailFormat(email string) bool {
	if len(email) > 254 {
		return false
	}
	_, err := mail.ParseAddress(email)
	if err != nil {
		return false
	}
	// additional regex check for correct email format
	emailRule := regexp.MustCompile(`^[a-zA-Z0-9._%+'-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)
	return emailRule.MatchString(email)
}

// BlurEmailAddress transforms an email address to reduce exposed personal info
func BlurEmailAddress(email string) string {
	items := strings.Split(email, "@")
	if len(items) < 1 || len(items[0]) < 1 {
		return "****@**"
	}

	blurredEmail := string([]rune(items[0])[0]) + "****@" + strings.Join(items[1:], "")
	return blurredEmail
}

// CheckPasswordFormat to check if password fulfills password rules
func CheckPasswordFormat(password string) bool {
	pl := len(password)
	if pl < 8 || pl > 512 {
		return false
	}

	var res = 0

	lowercase := regexp.MustCompile("[a-z]")
	uppercase := regexp.MustCompile("[A-Z]")
	number := regexp.MustCompile(`\d`) //"^(?:(?=.*[a-z])(?:(?=.*[A-Z])(?=.*[\\d\\W])|(?=.*\\W)(?=.*\d))|(?=.*\W)(?=.*[A-Z])(?=.*\d)).{8,}$")
	symbol := regexp.MustCompile(`\W`)

	if lowercase.MatchString(password) {
		res++
	}
	if uppercase.MatchString(password) {
		res++
	}
	if number.MatchString(password) {
		res++
	}
	if symbol.MatchString(password) {
		res++
	}
	return res > 2
}

// CheckLanguageCode checks if a string can be considered as a language code
func CheckLanguageCode(code string) bool {
	codeRule := regexp.MustCompile("^[a-z]{2}(-[a-zA-z]{2})?$")
	return codeRule.MatchString(code)
}

// IsTokenEmpty check a token from api if it's empty
func IsTokenEmpty(t *api_types.TokenInfos) bool {
	if t == nil || t.Id == "" || t.InstanceId == "" {
		return true
	}
	return false
}

// CheckRoleInToken Check if role is present in the token
func CheckRoleInToken(t *api_types.TokenInfos, role string) bool {
	if t == nil {
		return false
	}
	if val, ok := t.Payload["roles"]; ok {
		roles := strings.Split(val, ",")
		for _, r := range roles {
			if r == role {
				return true
			}
		}
	}
	return false
}

// SanitizePhone removes common non-numeric characters from the phone number.
// It removes spaces, hyphens, and parentheses to ensure a clean numeric format.
func SanitizePhone(phone string) string {
	cleaned := strings.ReplaceAll(phone, " ", "")
	cleaned = strings.ReplaceAll(cleaned, "-", "")
	cleaned = strings.ReplaceAll(cleaned, "(", "")
	cleaned = strings.ReplaceAll(cleaned, ")", "")

	return cleaned
}

// CheckPhoneFormat verifies if the given phone number matches the expected international format.
// The phone number must start with a '+' followed by 8 to 15 digits.
// Returns true if the format is valid, otherwise false.
func CheckPhoneFormat(phone string) bool {
	phoneRule := regexp.MustCompile(`^\+[0-9]{8,15}$`)
	return phoneRule.MatchString(phone)
}

func GenerateVerificationCode() string {
	// Generate a random 6-digit verification code
	return fmt.Sprintf("%06d", rand.Intn(1000000))
}
