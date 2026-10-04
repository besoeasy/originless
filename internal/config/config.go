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
	DefaultStorageMax = "20GB"
)

// Config holds every tunable setting. The IPFS endpoint and upload
// spool directory are fixed: the app always runs beside its Kubo daemon.
type Config struct {
	// StorageMax is the human-readable limit applied to the Kubo
	// repository, shared with the entrypoint. Parsed into
	// MaxEventBytes for the event store by FromEnv and Default.
	StorageMax string
	// MaxEventBytes bounds the total size of stored events. It is
	// StorageMax/5: events may use at most a fifth of the store's
	// storage budget. Zero means unbounded (used by tests).
	MaxEventBytes int64
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
	maxEventBytes, _ := storageMaxBytes(DefaultStorageMax)
	return Config{
		StorageMax:    DefaultStorageMax,
		MaxEventBytes: maxEventBytes / 5,
		EventsDBPath:  events.DefaultDBPath,
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
	if err != nil || amount <= 0 {
		return 0, fmt.Errorf("invalid size %q", value)
	}
	return int64(amount * float64(mult)), nil
}

// FromEnv builds a Config from the environment. getenv is called with the
// variable name and reports whether it was set; it is injected so the parsing
// rules can be tested without mutating the process environment.
func FromEnv(getenv func(string) (string, bool)) (Config, error) {
	cfg := Default()

	if value, ok := getenv("STORAGE_MAX"); ok && strings.TrimSpace(value) != "" {
		cfg.StorageMax = strings.TrimSpace(value)
		parsed, err := storageMaxBytes(cfg.StorageMax)
		if err != nil {
			return Config{}, fmt.Errorf("invalid STORAGE_MAX %q: %w", value, err)
		}
		cfg.MaxEventBytes = parsed / 5
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
	return fmt.Sprintf("storage_max=%q event_bytes_budget=%d events_db=%q sync_nodes=%d", c.StorageMax, c.MaxEventBytes, c.EventsDBPath, len(c.SyncNodes))
}
