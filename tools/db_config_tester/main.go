package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/coneno/logger"
	"github.com/influenzanet/go-utils/pkg/constants"
	"github.com/influenzanet/user-management-service/pkg/dbs/userdb"
	"github.com/influenzanet/user-management-service/pkg/models"
	"github.com/influenzanet/user-management-service/pkg/tokens"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

const INSTANCE_ID = "v1"

var userFilter = userdb.UserFilter{
	OnlyConfirmed:   false,
	ReminderWeekDay: -1,
}

var userDB *userdb.UserDBService

var benchmarkTimes []int64

func init() {
	conf := getDBConfig()
	userDB = userdb.NewUserDBService(conf)
}

func main() {
	parseFlags()
}

func parseFlags() {
	usersToGenerate := flag.Int("generateUsers", -1, "How many users to generate.")
	runBenchmark := flag.Bool("benchmark", false, "Whether to run the benchmark.")
	flag.Parse()

	if *usersToGenerate > 0 {
		generateUsers(*usersToGenerate)
	}
	if *runBenchmark {
		benchmark()
	}
}

func generateUsers(usersToGenerate int) {
	for a := 0; a < usersToGenerate; a++ {
		_, err := userDB.AddUser(INSTANCE_ID, generateRandomUser())
		if err != nil {
			logger.Error.Fatal(err)
		}
	}
	logger.Info.Printf("%v users generated", usersToGenerate)
}

func generateRandomUser() models.User {
	dummyID, _ := tokens.GenerateUniqueTokenString()
	return models.User{
		Account: models.Account{
			Type:               "email",
			AccountID:          dummyID,
			AccountConfirmedAt: time.Now().Unix(), // not confirmed yet
			Password:           dummyID,
			PreferredLanguage:  "no",
		},
		Roles: []string{constants.USER_ROLE_ADMIN},
		Profiles: []models.Profile{
			{
				ID:                 primitive.NewObjectID(),
				AvatarID:           "default",
				ConsentConfirmedAt: time.Now().Unix(),
				MainProfile:        true,
			},
		},
		Timestamps: models.Timestamps{
			CreatedAt: time.Now().Unix(),
		},
	}
}

func benchmark() {
	logger.Info.Println("Starting benchmark.")
	benchmarkTimes = nil
	recordBenchmarkTime()

	start := time.Now()
	/*
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
	*/
	ctx := context.Background()
	err := userDB.PerfomActionForUsers(ctx, INSTANCE_ID, userFilter, benchmarkCallback)
	if err != nil {
		logger.Error.Print(err.Error())
	}
	logger.Info.Printf("Finished running benchmark in %d seconds", time.Now().Unix()-start.Unix())

	saveBenchmarkTimesCSV()
}

func benchmarkCallback(instanceID string, user models.User, args ...interface{}) error {
	recordBenchmarkTime()
	return nil
}

func recordBenchmarkTime() {
	benchmarkTimes = append(benchmarkTimes, time.Now().UnixNano())
}

func saveBenchmarkTimesCSV() {
	file, err := os.OpenFile("./benchmark.csv", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)

	if err != nil {
		logger.Error.Fatalf("failed creating file: %s", err)
	}

	file.Truncate(0)
	file.Seek(0, 0)

	datawriter := bufio.NewWriter(file)
	datawriter.WriteString("Timestamp, Delta\n")

	for i, data := range benchmarkTimes {
		var reference int64

		if i > 0 {
			reference = benchmarkTimes[i-1]
		} else {
			reference = data
		}

		_, _ = datawriter.WriteString(strconv.FormatInt(data, 10) + "," + strconv.FormatInt(data-reference, 10) + "\n")
	}

	datawriter.Flush()
	file.Close()
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
