// Package config parses the environment-driven settings for Originless.
//
// Originless has no user accounts and no API authorization. The settings here
// are resource bounds, not access control, and every one has a working
// default.
package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Defaults for the environment-driven settings.
const (
	DefaultStorageMax = "20GB"
)

// Config holds every tunable setting. The IPFS API endpoint, gateway
// endpoint, and upload spool directory are fixed: the app always runs beside
// its Kubo daemon.
type Config struct {
	// StorageMax is the human-readable limit applied to the Kubo
	// repository, shared with the entrypoint.
	StorageMax string
}

// Default returns the configuration used when nothing is set in the
// environment.
func Default() Config {
	return Config{
		StorageMax: DefaultStorageMax,
	}
}

// storageMaxBytes parses a human-readable size like "20GB", "500MB" or
// "1.5TB" and returns it in bytes. Suffix-less values are bytes.
func storageMaxBytes(value string) (int64, error) {
	v := strings.ToUpper(strings.TrimSpace(value))
	if v == "" {
		return 0, fmt.Errorf("empty size")
	}
	mult := int64(1)
	for _, unit := range []struct {
		suffix string
		mult   int64
	}{
		{"TB", 1 << 40}, {"TIB", 1 << 40}, {"GB", 1 << 30}, {"GIB", 1 << 30},
		{"MB", 1 << 20}, {"MIB", 1 << 20}, {"KB", 1 << 10}, {"KIB", 1 << 10},
		{"T", 1 << 40}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10}, {"B", 1},
	} {
		if rest, ok := strings.CutSuffix(v, unit.suffix); ok {
			mult = unit.mult
			v = rest
			break
		}
	}
	v = strings.TrimSpace(v)
	amount, err := strconv.ParseFloat(v, 64)
	if err != nil || amount <= 0 || math.IsNaN(amount) || math.IsInf(amount, 0) {
		return 0, fmt.Errorf("invalid size %q", value)
	}
	bytes := int64(amount * float64(mult))
	if bytes < 1<<20 {
		return 0, fmt.Errorf("invalid size %q: must be at least 1MB", value)
	}
	return bytes, nil
}

// FromEnv builds a Config from the environment. getenv is called with the
// variable name and reports whether it was set; it is injected so the parsing
// rules can be tested without mutating the process environment.
func FromEnv(getenv func(string) (string, bool)) (Config, error) {
	cfg := Default()

	if value, ok := getenv("STORAGE_MAX"); ok && strings.TrimSpace(value) != "" {
		cfg.StorageMax = strings.TrimSpace(value)
		if _, err := storageMaxBytes(cfg.StorageMax); err != nil {
			return Config{}, fmt.Errorf("invalid STORAGE_MAX %q: %w", value, err)
		}
	}

	return cfg, nil
}

// Describe renders the settings, for the startup log line.
func (c Config) Describe() string {
	return fmt.Sprintf("storage_max=%q", c.StorageMax)
}
