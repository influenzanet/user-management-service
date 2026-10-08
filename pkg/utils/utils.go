package utils

import (
	"errors"
	"net/mail"
	"regexp"
	"strings"

	"github.com/influenzanet/go-utils/pkg/api_types"
	"github.com/nyaruka/phonenumbers"
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

// ErrPhoneNotValid reports that a phone number cannot be turned into a valid E.164 number.
var ErrPhoneNotValid = errors.New("phone not valid")

// phoneSeparators are the characters people put inside a number and that carry no digit.
var phoneSeparators = strings.NewReplacer(" ", "", "\t", "", "\n", "", "\r", "", "-", "", "(", "", ")", "", ".", "")

// NormalizePhone turns a phone number, in any of the forms people type it, into its E.164
// form ("+" and the digits, with no separators and no national trunk zero), which is the only
// form that is stored and compared.
//
// Accepted input is an international number, written with "+" or with the "00" international
// prefix, with any mix of spaces, dashes, dots and parentheses ("+44 (0)20 7946 0018",
// "0039 331 6221419"). The country code decides how the rest is read, so the trunk zero is
// dropped where the country has one (UK, DE, FR, NL, BE, CH, ...) and kept where it is part of
// the number (Italian landlines).
//
// A number without an international prefix is rejected instead of being read against a guessed
// region: the same digits are a different telephone in different countries, and the web client
// always sends the country code. The result must also be a valid number for its region.
func NormalizePhone(raw string) (string, error) {
	cleaned := phoneSeparators.Replace(raw)
	if strings.HasPrefix(cleaned, "00") {
		cleaned = "+" + cleaned[2:]
	}
	// Only "+" and ASCII digits are left at this point: letters (and so extensions) and any
	// other symbol make the number invalid, and so does a "+" anywhere but in front.
	if len(cleaned) < 2 || cleaned[0] != '+' {
		return "", ErrPhoneNotValid
	}
	for _, c := range cleaned[1:] {
		if c < '0' || c > '9' {
			return "", ErrPhoneNotValid
		}
	}
	// A country code never starts with zero, so "+00..." is a doubled prefix.
	if cleaned[1] == '0' {
		return "", ErrPhoneNotValid
	}

	num, err := phonenumbers.Parse(cleaned, "")
	if err != nil || !phonenumbers.IsValidNumber(num) {
		return "", ErrPhoneNotValid
	}
	return phonenumbers.Format(num, phonenumbers.E164), nil
}

// SamePhone tells whether two stored or received phone numbers are the same telephone. A value
// that cannot be normalised (data written before numbers were normalised) is only equal to
// itself.
func SamePhone(a, b string) bool {
	if a == b {
		return true
	}
	na, errA := NormalizePhone(a)
	nb, errB := NormalizePhone(b)
	return errA == nil && errB == nil && na == nb
}

// MaskPhone returns a masked phone number for logging (e.g. "+39***7890").
func MaskPhone(phone string) string {
	if len(phone) <= 6 {
		return "***"
	}
	return phone[:3] + "***" + phone[len(phone)-4:]
}
