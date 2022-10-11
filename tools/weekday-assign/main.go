package main

// "go.mongodb.org/mongo-driver/bson/primitive"

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"time"

	"github.com/coneno/logger"
	"github.com/influenzanet/user-management-service/internal/config"
	"github.com/influenzanet/user-management-service/pkg/dbs/userdb"
	"github.com/influenzanet/user-management-service/pkg/models"
)

var userDBService *userdb.UserDBService

type commandParams struct {
	instance string
	commit   bool
}

func init() {
	conf := config.GetUserDBConfig()
	userDBService = userdb.NewUserDBService(conf)
}

func loadParams() commandParams {
	instanceF := flag.String("instance", "", "Defines the instance ID.")
	commitF := flag.Bool("commit", false, "Commit the changes")

	flag.Parse()
	instance := *instanceF
	if instance == "" {
		logger.Error.Fatal("instance must be provided")
	}
	commit := *commitF
	return commandParams{instance: instance, commit: commit}
}

type weekdayCounter struct {
	counts []int
}

func newCounter() weekdayCounter {
	counts := make([]int, 7)
	return weekdayCounter{counts: counts}
}

func (c *weekdayCounter) add(day int) {
	c.counts[day] = 1 + c.counts[day]
}

func (c *weekdayCounter) String() string {
	return fmt.Sprintf(" %3d | %3d | %3d | %3d | %3d | %3d | %3d", c.counts[0], c.counts[1], c.counts[2], c.counts[3], c.counts[4], c.counts[5], c.counts[6])
}

func main() {

	rand.Seed(time.Now().UnixNano())

	weekdayStrategy := config.GetWeekDayStrategy()

	params := loadParams()

	userFilter := userdb.UserFilter{
		OnlyConfirmed:   false,
		ReminderWeekDay: -1,
	}

	before := newCounter()
	after := newCounter()

	count_scanned := 0

	ctx := context.Background()
	err := userDBService.PerfomActionForUsers(ctx, params.instance, userFilter, func(instanceID string, user models.User, args ...interface{}) error {

		//fmt.Printf("user %s %d\n", user.ID, user.ContactPreferences.ReceiveWeeklyMessageDayOfWeek)

		day := int(user.ContactPreferences.ReceiveWeeklyMessageDayOfWeek)
		before.add(int(day))

		newDay := weekdayStrategy.Weekday()
		after.add(newDay)

		if params.commit {
			// Do update
			user.ContactPreferences.ReceiveWeeklyMessageDayOfWeek = int32(newDay)
			_, e := userDBService.UpdateUser(instanceID, user)
			if e != nil {
				logger.Error.Printf("updating user %s : %s", user.ID, e)
			}
		}

		count_scanned += 1

		return nil
	})

	fmt.Printf("Scanned users %d\n", count_scanned)

	if err != nil {
		logger.Error.Printf(err.Error())
	}

	fmt.Println("        Sun | Mon | Tue | Wed | Thu | Fri | Sat")
	fmt.Printf("Before %s\n", before.String())
	fmt.Printf("After  %s\n", after.String())
	if params.commit {
		fmt.Println("Changes applied")
	} else {
		fmt.Println("Changes NOT applied (use --commit flag to apply changes)")
	}

}
