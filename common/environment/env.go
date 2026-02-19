// Package environment provides helpers for loading configuration from environment variables.
//
// All helpers follow a consistent pattern: they read an environment variable and
// return either the value or a default. Required variables return an error rather
// than calling os.Exit, keeping business logic out of library code.
package environment

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// String returns the value of the named environment variable and a boolean
// indicating whether it was set (even if set to the empty string).
func String(name string) (string, bool) {
	v, ok := os.LookupEnv(name)
	return v, ok
}

// StringOr returns the value of the named environment variable, or defaultValue
// if the variable is unset or empty.
func StringOr(name, defaultValue string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return defaultValue
}

// RequiredString returns the value of the named environment variable or an error
// if it is unset or empty.
func RequiredString(name string) (string, error) {
	v := os.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("required environment variable %q is not set", name)
	}
	return v, nil
}

// BoolOr parses the named environment variable as a boolean. Recognized values
// are the same as strconv.ParseBool ("1", "t", "true", "0", "f", "false", etc.).
// Returns defaultValue if the variable is unset, empty, or cannot be parsed.
func BoolOr(name string, defaultValue bool) bool {
	v := os.Getenv(name)
	if v == "" {
		return defaultValue
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return defaultValue
	}
	return b
}

// IntOr parses the named environment variable as a decimal integer. Returns
// defaultValue if the variable is unset, empty, or cannot be parsed.
func IntOr(name string, defaultValue int) int {
	v := os.Getenv(name)
	if v == "" {
		return defaultValue
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return defaultValue
	}
	return n
}

// DurationOr parses the named environment variable as a time.Duration (e.g.
// "30s", "5m", "1h"). Returns defaultValue if the variable is unset, empty,
// or cannot be parsed.
func DurationOr(name string, defaultValue time.Duration) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return defaultValue
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return defaultValue
	}
	return d
}

// StringSliceOr parses the named environment variable as a comma-separated list
// of strings, trimming whitespace from each element. Returns defaultValue if the
// variable is unset or empty.
func StringSliceOr(name string, defaultValue []string) []string {
	v := os.Getenv(name)
	if v == "" {
		return defaultValue
	}
	parts := strings.Split(v, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			result = append(result, t)
		}
	}
	if len(result) == 0 {
		return defaultValue
	}
	return result
}
