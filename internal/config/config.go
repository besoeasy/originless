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

	"github.com/besoeasy/originless/internal/events"
)

// Defaults for the environment-driven settings.
const (
	DefaultMaxEvents    = 10000
)

// Config holds every tunable setting. The IPFS endpoint and upload
// spool directory are fixed: the app always runs beside its Kubo daemon.
type Config struct {
	// MaxEvents bounds in-memory event growth. The oldest live events are
	// evicted once the limit is reached. This is the same class of bound as
	// the per-event size and TTL limits, not an access control.
	MaxEvents int
	// EventsDBPath is where signed events are persisted in SQLite. It always
	// has a working value; there is no memory-only mode.
	EventsDBPath string
	// SyncNodes are base URLs of peer Originless nodes to federate with
	// (bidirectional pull sync over HTTP).
	SyncNodes []string
}

// Default returns the configuration used when nothing is set in the
// environment.
func Default() Config {
	return Config{
		MaxEvents:    DefaultMaxEvents,
		EventsDBPath: events.DefaultDBPath,
	}
}

// FromEnv builds a Config from the environment. getenv is called with the
// variable name and reports whether it was set; it is injected so the parsing
// rules can be tested without mutating the process environment.
func FromEnv(getenv func(string) (string, bool)) (Config, error) {
	cfg := Default()

	if value, ok := getenv("MAX_EVENTS"); ok && strings.TrimSpace(value) != "" {
		maxEvents, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || maxEvents < 1 {
			return Config{}, fmt.Errorf("invalid MAX_EVENTS %q: must be a positive integer", value)
		}
		cfg.MaxEvents = maxEvents
	}

	if value, ok := getenv("SYNC_NODES"); ok && strings.TrimSpace(value) != "" {
		for _, node := range strings.Split(value, ",") {
			node = strings.TrimSpace(node)
			if node == "" {
				continue
			}
			if !strings.HasPrefix(node, "http://") && !strings.HasPrefix(node, "https://") {
				return Config{}, fmt.Errorf("invalid SYNC_NODES entry %q: must start with http:// or https://", node)
			}
			cfg.SyncNodes = append(cfg.SyncNodes, node)
		}
	}

	return cfg, nil
}

// Describe renders the settings, for the startup log line.
func (c Config) Describe() string {
	return fmt.Sprintf("max_events=%d events_db=%q sync_nodes=%d", c.MaxEvents, c.EventsDBPath, len(c.SyncNodes))
}
