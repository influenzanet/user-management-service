package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/coneno/logger"
)

// parseDuration
func parseDuration(value string, defaultUnit string) (time.Duration, error) {
	// Provided value is only a numeric string
	if v, err := strconv.Atoi(value); err == nil {
		value = fmt.Sprintf("%d"+defaultUnit, v)
	}

	d, err := time.ParseDuration(value)
	if err != nil {
		return time.Duration(0), fmt.Errorf("invalid time duration '%s' : %s", value, err.Error())
	}
	return d, nil
}

func parseEnvDuration(name string, defaultValue time.Duration, defaultUnit string) time.Duration {
	value := os.Getenv(name)
	if value == "" {
		logger.Info.Printf("%s : not provided using default value %s", name, defaultValue)
		return defaultValue
	}
	d, err := parseDuration(value, defaultUnit)
	if err != nil {
		logger.Error.Printf("%s : unexpected error - default value used, %s", name, err.Error())
		return defaultValue
	}
	logger.Info.Printf("%s : using value %s", name, d)
	return d
}
