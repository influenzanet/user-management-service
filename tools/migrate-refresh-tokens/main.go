package main

// "go.mongodb.org/mongo-driver/bson/primitive"

import (
	b32 "encoding/base32"
	"encoding/binary"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/coneno/logger"
	"github.com/influenzanet/user-management-service/internal/config"
	"github.com/influenzanet/user-management-service/pkg/dbs/userdb"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var userDBService *userdb.UserDBService

type commandParams struct {
	instance string
	minTime  uint64
	commit   bool
}

type ParsedRefreshToken struct {
	UserID string
	Token  string
	Time   uint64
}

func GetRefreshTokens(svc *userdb.UserDBService, instanceID string, minTime uint64) ([]ParsedRefreshToken, error) {
	// {profiles: {"$elemMatch": {"mainProfile": true}}}
	ctx, cancel := svc.GetContext()
	defer cancel()

	users := svc.GetCollection(instanceID, userdb.UserCollection)

	filter := bson.M{"account.accountConfirmedAt": bson.M{"$gt": 0}}
	opts := options.Find()
	cursor, err := users.Find(ctx, filter, opts)

	if err != nil {
		return nil, err
	}

	var results []bson.M
	if err = cursor.All(ctx, &results); err != nil {
		return nil, err
	}

	output := make([]ParsedRefreshToken, 0, len(results))

	for _, result := range results {
		id := result["_id"].(primitive.ObjectID).Hex()
		account := result["account"].(bson.M)

		if account["refreshTokens"] == nil {
			continue
		}

		tokens := account["refreshTokens"].(bson.A)

		if len(tokens) > 0 {
			for idx, tokenObj := range tokens {
				var time uint64
				token := tokenObj.(string)
				parsed, err := b32.StdEncoding.WithPadding(b32.NoPadding).DecodeString(strings.ToUpper(token))
				if err == nil {
					var t [8]byte
					copy(t[2:7], parsed[0:5])
					time = binary.BigEndian.Uint64(t[:])
				} else {
					fmt.Printf("Unable to decode token '%s' for %s at %d : %s\n", token, id, idx, err)
					continue
				}

				if time < minTime {
					fmt.Printf("Time is below min time %d for '%s' for %s at %d\n", time, token, id, idx)
					continue
				}

				m := ParsedRefreshToken{
					UserID: id,
					Token:  token,
					Time:   time / 1000,
				}
				output = append(output, m)
			}
		}

	}
	return output, nil
}

type RefreshResults struct {
	Created int64
	Expired int64
	Error   int64
}

func CreateRefreshToken(svc *userdb.UserDBService, instanceID string, tokens []ParsedRefreshToken, duration int64, commit bool) RefreshResults {

	now := time.Now().Unix()

	r := RefreshResults{}

	for idx, token := range tokens {
		expires := int64(token.Time) + duration
		if expires > now {
			fmt.Printf("Expired at %d %s %s\n", idx, token.UserID, token.Token)
			r.Expired++
			continue
		}

		if commit {

			err := createToken(svc, instanceID, token, expires)

			if err != nil {
				fmt.Printf("<Error> at %d %s %s : %s\n", idx, token.UserID, token.Token, err)
				r.Error++
			} else {
				r.Created++
			}
		} else {
			r.Created++
		}
	}
	return r
}

func createToken(svc *userdb.UserDBService, instanceID string, token ParsedRefreshToken, expires int64) error {
	coll := svc.GetCollection(instanceID, userdb.RenewTokenCollection)
	ctx, cancel := svc.GetContext()
	defer cancel()
	doc := bson.M{
		"userID":     token.UserID,
		"renewToken": token.Token,
		"expiresAt":  expires,
	}
	_, err := coll.InsertOne(ctx, doc)
	return err
}

func init() {
	conf := config.GetUserDBConfig()
	userDBService = userdb.NewUserDBService(conf)
}

func loadParams() commandParams {
	instanceF := flag.String("instance", "", "Defines the instance ID.")
	minTimeF := flag.String("datemin", "", "Minimal date acceptable for token")
	commitF := flag.Bool("commit", false, "Commit the changes")

	p := commandParams{}

	flag.Parse()
	instance := *instanceF
	if instance == "" {
		logger.Error.Fatal("instance must be provided")
	}
	min := *minTimeF
	if min != "" {
		minTime, err := time.Parse("2006-01-02", min)
		if err != nil {
			logger.Error.Fatal("Wrong date format")
		}
		p.minTime = uint64(minTime.Unix())
	}
	p.commit = *commitF
	p.instance = instance
	return p
}

func main() {
	params := loadParams()
	minTime := params.minTime
	instanceID := params.instance

	tokens, err := GetRefreshTokens(userDBService, instanceID, minTime)

	if err != nil {
		fmt.Println(err)
		return
	}

	r := CreateRefreshToken(userDBService, instanceID, tokens, userdb.RENEW_TOKEN_DEFAULT_LIFETIME, params.commit)
	if !params.commit {
		fmt.Println("Using dry-run mode, nothing is done in the db, to make change, add -commit to the command line")
	}
	fmt.Printf("Created: %d, Error:%d, Expired: %d", r.Created, r.Error, r.Expired)
}
