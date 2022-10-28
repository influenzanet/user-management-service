package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"time"
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
		log.Printf("%s : not provided using default value %s", name, defaultValue.String())
		return defaultValue
	}
	d, err := parseDuration(value, defaultUnit)
	if err != nil {
		log.Printf("%s : default value used, %s", name, err.Error())
		return defaultValue
	}
	log.Printf("%s : using value %s", name, d.String())
	return d
}
