// Package config parses the environment-driven settings for Originless.
//
// Originless has no user accounts and no API authorization: an Ed25519
// signature on an event is the only credential in the protocol, and it proves
// authorship rather than permission. The settings here are therefore resource
// bounds, not access control, and every one has a working default.
package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Defaults for the environment-driven settings.
const (
	DefaultMaxEvents    = 10000
	DefaultUploadTmpDir = ""
)

// Config holds every tunable setting.
type Config struct {
	// UploadTmpDir is where multipart parts are spooled. Empty means the
	// system temporary directory.
	UploadTmpDir string
	// MaxEvents bounds in-memory event growth. The oldest live events are
	// evicted once the limit is reached. This is the same class of bound as
	// the per-event size and TTL limits, not an access control.
	MaxEvents int
	// EventsDBPath is where signed events are persisted in SQLite. Empty
	// means memory-only: events do not survive restarts.
	EventsDBPath string
}

// Default returns the configuration used when nothing is set in the
// environment.
func Default() Config {
	return Config{
		UploadTmpDir: DefaultUploadTmpDir,
		MaxEvents:    DefaultMaxEvents,
	}
}

// FromEnv builds a Config from the environment. getenv is called with the
// variable name and reports whether it was set; it is injected so the parsing
// rules can be tested without mutating the process environment.
func FromEnv(getenv func(string) (string, bool)) (Config, error) {
	cfg := Default()

	if value, ok := getenv("UPLOAD_TMPDIR"); ok {
		cfg.UploadTmpDir = strings.TrimSpace(value)
	}

	if value, ok := getenv("MAX_EVENTS"); ok && strings.TrimSpace(value) != "" {
		maxEvents, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || maxEvents < 1 {
			return Config{}, fmt.Errorf("invalid MAX_EVENTS %q: must be a positive integer", value)
		}
		cfg.MaxEvents = maxEvents
	}

	if value, ok := getenv("EVENTS_DB_PATH"); ok {
		cfg.EventsDBPath = strings.TrimSpace(value)
	}

	return cfg, nil
}

// Describe renders the settings, for the startup log line.
func (c Config) Describe() string {
	return fmt.Sprintf("max_events=%d upload_tmpdir=%q events_db=%q", c.MaxEvents, c.UploadTmpDir, c.EventsDBPath)
}
