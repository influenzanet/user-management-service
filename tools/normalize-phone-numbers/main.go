package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/coneno/logger"
	"github.com/influenzanet/user-management-service/internal/config"
	"github.com/influenzanet/user-management-service/pkg/dbs/userdb"
)

type commandParams struct {
	instances []string
	apply     bool
}

// parseParams reads the command line. Writing needs -apply: -dry-run only states the default, so
// asking for "no dry run" without -apply is refused instead of being read as either of the two.
func parseParams(args []string) (commandParams, error) {
	fs := flag.NewFlagSet("normalize-phone-numbers", flag.ContinueOnError)
	instanceF := fs.String("instance", "", "Defines the instance ID (several instances can be given, separated by commas).")
	applyF := fs.Bool("apply", false, "Write the changes. Without it nothing is written (dry run).")
	dryRunF := fs.Bool("dry-run", true, "Only report what would change. This is the default; it cannot be combined with -apply.")

	if err := fs.Parse(args); err != nil {
		return commandParams{}, err
	}

	dryRunSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "dry-run" {
			dryRunSet = true
		}
	})
	if *applyF && dryRunSet && *dryRunF {
		return commandParams{}, errors.New("-apply and -dry-run cannot be combined")
	}
	if dryRunSet && !*dryRunF && !*applyF {
		return commandParams{}, errors.New("-dry-run=false does not write anything by itself: add -apply to write the changes, or drop -dry-run to only get the report")
	}

	p := commandParams{apply: *applyF}
	for _, id := range strings.Split(*instanceF, ",") {
		if id = strings.TrimSpace(id); id != "" {
			p.instances = append(p.instances, id)
		}
	}
	if len(p.instances) == 0 {
		return commandParams{}, errors.New("instance must be provided")
	}
	return p, nil
}

func loadParams() commandParams {
	p, err := parseParams(os.Args[1:])
	if err != nil {
		logger.Error.Fatal(err)
	}
	return p
}

func printReport(r Report) {
	mode := "dry run, nothing written"
	if r.Applied {
		mode = "applied"
	}
	fmt.Printf("== Instance %s (%s)\n", r.Instance, mode)
	fmt.Printf("Accounts with a phone number: %d\n", r.UsersScanned)
	fmt.Printf("Already normalised: %d\n", r.AlreadyNormalized)
	if r.Applied {
		fmt.Printf("Changed: %d\n", r.Changed)
		fmt.Printf("Skipped, changed meanwhile: %d\n", r.SkippedConcurrent)
	} else {
		fmt.Printf("Would change: %d\n", len(r.Changes))
	}
	for _, c := range r.Changes {
		hint := ""
		if c.TrunkZeroRemoved {
			hint = " (trunk zero removed)"
		}
		fmt.Printf("  change  user=%s field=%s %s -> %s digits %d -> %d%s\n", c.UserID, c.Field, c.Old, c.New, c.OldDigits, c.NewDigits, hint)
	}
	fmt.Printf("Unparseable, left untouched: %d\n", len(r.Unparseable))
	for _, u := range r.Unparseable {
		fmt.Printf("  unparseable  user=%s field=%s %s\n", u.UserID, u.Field, u.Number)
	}
	fmt.Printf("Collisions, accounts left untouched: %d\n", len(r.Collisions))
	for _, c := range r.Collisions {
		fmt.Printf("  collision  %s accounts=%s verified=%d\n", c.Number, strings.Join(c.UserIDs, ","), c.Verified)
	}
	fmt.Printf("Notes, shared with one verified holder, all accounts normalised: %d\n", len(r.Notes))
	for _, n := range r.Notes {
		fmt.Printf("  note  %s accounts=%s verified=%s\n", n.Number, strings.Join(n.UserIDs, ","), n.Verified)
	}
	if r.Applied {
		fmt.Printf("Claims seeded: %d\n", r.ClaimsSeeded)
	} else {
		fmt.Printf("Claims that would be seeded: %d\n", r.ClaimsToSeed)
	}
	fmt.Printf("Claims already in place: %d\n", r.ClaimsPresent)
	fmt.Printf("Claims held by another account, left untouched: %d\n", len(r.ClaimConflicts))
	for _, c := range r.ClaimConflicts {
		fmt.Printf("  claim conflict  %s verified holder=%s claim owner=%s\n", c.Number, c.UserID, c.OwnerID)
	}
}

func main() {
	params := loadParams()
	svc := userdb.NewUserDBService(config.GetUserDBConfig())

	failed := false
	for _, instance := range params.instances {
		report, err := NormalizePhoneNumbers(svc, instance, params.apply)
		printReport(report)
		if err != nil {
			logger.Error.Printf("instance %s: %v", instance, err)
			failed = true
		}
	}
	if !params.apply {
		fmt.Println("Dry run: nothing was written. Add -apply to write the changes.")
	}
	if failed {
		logger.Error.Fatal("at least one instance failed")
	}
}
