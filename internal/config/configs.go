package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/coneno/logger"
	"github.com/influenzanet/user-management-service/pkg/models"
	"github.com/influenzanet/user-management-service/pkg/utils"
)

// Config is the structure that holds all global configuration data
type Config struct {
	LogLevel    logger.LogLevel
	Port        string
	ServiceURLs struct {
		MessagingService string
		LoggingService   string
		StudyService     string
	}
	UserDBConfig                      models.DBConfig
	GlobalDBConfig                    models.DBConfig
	Intervals                         models.Intervals
	NewUserCountLimit                 int64
	CleanUpUnverifiedUsersAfter       int64
	ReminderToUnverifiedAccountsAfter int64
	NotifyInactiveUsersAfter          int64
	DeleteAccountAfterNotifyingUser   int64

	WeekDayStrategy utils.WeekDayStrategy

	DisableTimerTask bool
}

func InitConfig() Config {
	conf := Config{}
	conf.Port = os.Getenv(ENV_USER_MANAGEMENT_LISTEN_PORT)
	conf.ServiceURLs.MessagingService = os.Getenv(ENV_ADDR_MESSAGING_SERVICE)
	conf.ServiceURLs.LoggingService = os.Getenv(ENV_ADDR_LOGGING_SERVICE)
	conf.ServiceURLs.StudyService = os.Getenv(ENV_ADDR_STUDY_SERVICE)
	if conf.ServiceURLs.StudyService == "" {
		logger.Warning.Printf("Address of study service: not provided, can not connect to study service")
	}

	conf.LogLevel = getLogLevel()
	conf.UserDBConfig = GetUserDBConfig()
	conf.GlobalDBConfig = GetGlobalDBConfig()
	conf.Intervals = getIntervalsConfig()

	rl, err := strconv.Atoi(os.Getenv(ENV_NEW_USER_RATE_LIMIT))
	if err != nil {
		logger.Error.Fatal(ENV_NEW_USER_RATE_LIMIT, ":"+err.Error())
	}
	conf.NewUserCountLimit = int64(rl)

	cleanUpThreshold, err := strconv.Atoi(os.Getenv(ENV_CLEAN_UP_UNVERIFIED_USERS_AFTER))
	if err != nil {
		logger.Error.Fatal(ENV_CLEAN_UP_UNVERIFIED_USERS_AFTER, ":"+err.Error())
	}
	conf.CleanUpUnverifiedUsersAfter = int64(cleanUpThreshold)

	reminderToUnverifiedAccountsAfter, err := strconv.Atoi(os.Getenv(ENV_SEND_REMINDER_TO_UNVERIFIED_USERS_AFTER))
	if err != nil {
		logger.Error.Fatal(ENV_SEND_REMINDER_TO_UNVERIFIED_USERS_AFTER + ": " + err.Error())
	}
	conf.ReminderToUnverifiedAccountsAfter = int64(reminderToUnverifiedAccountsAfter)

	notifyInactiveUsersAfter, err := strconv.Atoi(os.Getenv(ENV_NOTIFY_INACTIVE_USERS_AFTER))
	if err != nil {
		logger.Info.Printf(ENV_NOTIFY_INACTIVE_USERS_AFTER + ": not provided, inactive users will be ignored")
		conf.NotifyInactiveUsersAfter = defaultNotifyInactiveUsersAfter
	}
	conf.NotifyInactiveUsersAfter = int64(notifyInactiveUsersAfter)

	deleteAccountAfterNotifyingUser, err := strconv.Atoi(os.Getenv(ENV_DELETE_ACCOUNT_AFTER_NOTIFYING_USER))
	if err != nil {
		logger.Info.Printf(ENV_DELETE_ACCOUNT_AFTER_NOTIFYING_USER + ": not provided, inactive users will be ignored")
		conf.DeleteAccountAfterNotifyingUser = defaultDeleteAccountAfterNotifyingUser
	}
	conf.DeleteAccountAfterNotifyingUser = int64(deleteAccountAfterNotifyingUser)

	conf.WeekDayStrategy = GetWeekDayStrategy()

	conf.DisableTimerTask = os.Getenv(ENV_DISABLE_TIMER_TASK) == "true"
	return conf
}

// Get Weekday attribution strategy
func GetWeekDayStrategy() utils.WeekDayStrategy {

	wday := os.Getenv(ENV_WEEKDAY_ASSIGNATION_WEIGHTS)
	if wday == "" {
		return utils.CreateWeekdayDefaultStrategy()
	}
	w, err := utils.ParseWeeklyWeight(wday)
	if err != nil {
		logger.Error.Fatalf("%s : %s", ENV_WEEKDAY_ASSIGNATION_WEIGHTS, err)
	}

	strategy := utils.CreateWeekdayWeightedStrategy(w)
	fmt.Println("Weekday Strategy: ", strategy.String())
	return strategy
}

func getLogLevel() logger.LogLevel {
	switch os.Getenv(ENV_LOG_LEVEL) {
	case "debug":
		return logger.LEVEL_DEBUG
	case "info":
		return logger.LEVEL_INFO
	case "error":
		return logger.LEVEL_ERROR
	case "warning":
		return logger.LEVEL_WARNING
	default:
		return logger.LEVEL_INFO
	}
}

func getIntervalsConfig() models.Intervals {
	intervals := models.Intervals{
		TokenExpiryInterval:      time.Minute * time.Duration(defaultTokenExpirationMin),
		VerificationCodeLifetime: defaultVerificationCodeLifetime,
	}

	accessTokenExpiration, err := strconv.Atoi(os.Getenv(ENV_TOKEN_EXPIRATION_MIN))
	if err != nil {
		logger.Info.Printf("using default token expiration: %s", intervals.TokenExpiryInterval)
	} else {
		intervals.TokenExpiryInterval = time.Minute * time.Duration(accessTokenExpiration)
	}

	newVerificationCodeLifetime, err := strconv.Atoi(os.Getenv(ENV_VERIFICATION_CODE_LIFETIME))
	if err != nil {
		logger.Info.Println("using default verification code lifetime")
	} else {
		intervals.VerificationCodeLifetime = int64(newVerificationCodeLifetime)
	}

	intervals.InvitationTokenLifetime = parseEnvDuration(ENV_TOKEN_INVITATION_LIFETIME, defaultInvitationTokenLifetime, "m")

	intervals.ContactVerificationTokenLifetime = parseEnvDuration(ENV_TOKEN_CONTACT_VERIFICATION_LIFETIME, defaultContactVerificationTokenLifetime, "m")

	return intervals
}
