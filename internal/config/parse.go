package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// ParseBool parses 1/0, true/false, yes/no, on/off case-insensitively.
func ParseBool(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}

// ParseDuration parses plain seconds or durations with ms/s/m/h suffixes.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if secs, err := strconv.ParseFloat(s, 64); err == nil {
		if secs < 0 {
			return 0, fmt.Errorf("negative duration %q", s)
		}
		return time.Duration(secs * float64(time.Second)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	return d, nil
}

// ParseBytes parses byte counts with 1024-base k/m suffixes.
func ParseBytes(s string) (int64, error) {
	return parseScaledInt(s, 1024)
}

// ParseTokens parses token counts with decimal (1000-base) k/m suffixes.
func ParseTokens(s string) (int64, error) {
	return parseScaledInt(s, 1000)
}

func parseScaledInt(s string, base int64) (int64, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "k"):
		mult = base
		s = strings.TrimSuffix(s, "k")
	case strings.HasSuffix(s, "m"):
		mult = base * base
		s = strings.TrimSuffix(s, "m")
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return n * mult, nil
}

// ExpandEnvValue expands "$VAR" (reading env) and "$$literal" (escaping "$").
func ExpandEnvValue(val string) (string, bool) {
	if strings.HasPrefix(val, "$$") {
		return val[1:], true
	}
	if strings.HasPrefix(val, "$") && len(val) > 1 {
		name := val[1:]
		v, ok := os.LookupEnv(name)
		return v, ok && v != ""
	}
	return val, true
}
