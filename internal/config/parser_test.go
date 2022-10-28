package config

import (
	"fmt"
	"testing"
	"time"
)

func test_duration(t *testing.T, value string, defaultUnit string, expect time.Duration, expect_err bool) {
	t.Run(fmt.Sprintf("parse duration %s", value), func(t *testing.T) {
		d, err := parseDuration(value, defaultUnit)
		if err != nil {
			if expect_err {
				return
			}
			t.Error(err)
		}
		if d != expect {
			t.Errorf("Bad duration parsed got %s, expect %s", d.String(), expect.String())
		}
	})
}

func TestParseDuration(t *testing.T) {

	t.Run("test_with_duration", func(t *testing.T) {
		test_duration(t, "1m", "m", 1*time.Minute, false)
		test_duration(t, "5m", "m", 5*time.Minute, false)
		test_duration(t, "5h", "m", 5*time.Hour, false)
	})

	t.Run("test_with_empty_duration", func(t *testing.T) {
		test_duration(t, "3", "m", 3*time.Minute, false)
		test_duration(t, "4", "h", 4*time.Hour, false)
	})
}

func test_env_duration(t *testing.T, name string, value string, defaultDuration time.Duration, defaultUnit string, expect time.Duration) {
	t.Run(fmt.Sprintf("parse duration %s", value), func(t *testing.T) {
		t.Setenv(name, value)
		d := parseEnvDuration(name, defaultDuration, defaultUnit)
		if d != expect {
			t.Errorf("Invalid time got '%s' expect '%s'", d.String(), expect.String())
		}
	})
}

func TestParseEnvDuration(t *testing.T) {
	test_env_duration(t, "test_env", "123", time.Minute, "h", 123*time.Hour)
	test_env_duration(t, "test_env", "", 18*time.Minute, "h", 18*time.Minute)
	test_env_duration(t, "test_env", "5h", 18*time.Minute, "h", 5*time.Hour)
}
