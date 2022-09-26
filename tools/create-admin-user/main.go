package main

import (
	"bufio"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"syscall"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
	"golang.org/x/term"

	"github.com/coneno/logger"
	"github.com/influenzanet/user-management-service/pkg/models"

	"github.com/influenzanet/go-utils/pkg/constants"

	"github.com/influenzanet/user-management-service/pkg/pwhash"
	"github.com/influenzanet/user-management-service/pkg/utils"

	"github.com/influenzanet/user-management-service/pkg/dbs/userdb"
)

type UserRequest struct {
	email      string `yaml:"email"`
	password   string `yaml:"password"`
	use2FA     bool   `yaml:"use2FA"`
	instanceID string `yaml:"instance"`
	language   string `yaml:"language"`
	weekday    int    `yaml:"weekday"`
}

var userDBService *userdb.UserDBService

func reqfromCLI() (UserRequest, error) {
	req := UserRequest{}
	instanceF := flag.String("instance", "", "Defines the instance ID.")
	language := flag.String("language", "en", "Define the default language for the new user")
	use2FA := flag.Bool("use2FA", false, "If the account should be creating with auth type 2FA.")
	weekday := flag.Int("weekday", rand.Intn(7), "Weekday to assign to the user")
	flag.Parse()

	req.instanceID = *instanceF
	req.language = *language
	req.use2FA = *use2FA
	req.weekday = *weekday

	if req.instanceID == "" {
		return req, fmt.Errorf("instance must be provided")
	}

	return req, nil
}

func init() {
	conf := getDBConfig()
	userDBService = userdb.NewUserDBService(conf)
}

func main() {
	req, err := reqfromCLI()
	if err != nil {
		logger.Error.Fatal(err.Error())
	}

	username, password, err := getCredentials()
	if err != nil {
		logger.Error.Fatal(err.Error())
	}

	req.email = utils.SanitizeEmail(username)
	req.password = password

	if !utils.CheckEmailFormat(req.email) {
		logger.Error.Fatal("account id not a valid email")
	}
	if !utils.CheckPasswordFormat(req.password) {
		logger.Error.Fatal("password too weak")
	}

	hashedPassword, err := pwhash.HashPassword(req.password)
	if err != nil {
		logger.Error.Fatal(err.Error())
	}

	authType := ""
	if req.use2FA {
		authType = "2FA"
	}
	newUser := models.User{
		Account: models.Account{
			Type:               "email",
			AccountID:          req.email,
			AccountConfirmedAt: time.Now().Unix(),
			Password:           hashedPassword,
			PreferredLanguage:  req.language,
			AuthType:           authType,
		},
		Roles: []string{constants.USER_ROLE_ADMIN},
		Profiles: []models.Profile{
			{
				ID:                 primitive.NewObjectID(),
				Alias:              utils.BlurEmailAddress(req.email),
				AvatarID:           "default",
				ConsentConfirmedAt: time.Now().Unix(),
				MainProfile:        true,
			},
		},
		Timestamps: models.Timestamps{
			CreatedAt: time.Now().Unix(),
		},
	}
	newUser.AddNewEmail(req.email, true)
	newUser.ContactPreferences.SubscribedToNewsletter = true
	newUser.ContactPreferences.SendNewsletterTo = []string{newUser.ContactInfos[0].ID.Hex()}
	newUser.ContactPreferences.SubscribedToWeekly = true
	newUser.ContactPreferences.ReceiveWeeklyMessageDayOfWeek = int32(req.weekday)

	instanceID := req.instanceID
	id, err := userDBService.AddUser(instanceID, newUser)
	if err != nil {
		logger.Error.Fatal(err.Error())
	}
	newUser.ID, _ = primitive.ObjectIDFromHex(id)

	fmt.Printf("User created with id : %s", id)
}

func getCredentials() (string, string, error) {
	reader := bufio.NewReader(os.Stdin)

	fmt.Print("Enter an Email for the username: ")
	username, err := reader.ReadString('\n')
	if err != nil {
		return "", "", err
	}

	fmt.Print("Enter Password: ")
	bytePassword, err := term.ReadPassword(int(syscall.Stdin))
	if err != nil {
		return "", "", err
	}

	fmt.Print("\nConfirm Password: ")
	bytePassword2, err := term.ReadPassword(int(syscall.Stdin))
	if err != nil {
		return "", "", err
	}

	password := string(bytePassword)
	confirmPassword := string(bytePassword2)
	if confirmPassword != password {
		return "", "", fmt.Errorf("Passwords don't match.")
	}
	return username, password, nil
}

func getDBConfig() models.DBConfig {
	connStr := os.Getenv("USER_DB_CONNECTION_STR")
	username := os.Getenv("USER_DB_USERNAME")
	password := os.Getenv("USER_DB_PASSWORD")
	prefix := os.Getenv("USER_DB_CONNECTION_PREFIX") // Used in test mode
	URI := fmt.Sprintf(`mongodb%s://%s:%s@%s`, prefix, username, password, connStr)
	if username == "" || password == "" {
		URI = fmt.Sprintf(`mongodb%s://%s`, prefix, connStr)
	}

	var err error
	Timeout, err := strconv.Atoi(os.Getenv("DB_TIMEOUT"))
	if err != nil {
		logger.Error.Fatal("DB_TIMEOUT: " + err.Error())
	}
	IdleConnTimeout, err := strconv.Atoi(os.Getenv("DB_IDLE_CONN_TIMEOUT"))
	if err != nil {
		logger.Error.Fatal("DB_IDLE_CONN_TIMEOUT" + err.Error())
	}
	mps, err := strconv.Atoi(os.Getenv("DB_MAX_POOL_SIZE"))
	MaxPoolSize := uint64(mps)
	if err != nil {
		logger.Error.Fatal("DB_MAX_POOL_SIZE: " + err.Error())
	}

	noCursorTimeout := os.Getenv("USE_NO_CURSOR_TIMEOUT") == "true"

	DBNamePrefix := os.Getenv("DB_DB_NAME_PREFIX")

	return models.DBConfig{
		URI:             URI,
		Timeout:         Timeout,
		IdleConnTimeout: IdleConnTimeout,
		NoCursorTimeout: noCursorTimeout,
		MaxPoolSize:     MaxPoolSize,
		DBNamePrefix:    DBNamePrefix,
	}
}
