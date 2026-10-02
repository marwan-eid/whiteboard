// Package config loads node configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	// Addr is the HTTP listen address (ADDR, default ":8081").
	Addr string
	// DatabaseURL is the Postgres connection string (DATABASE_URL, required).
	DatabaseURL string
	// NodeID identifies this node to clients and, later, in board leases
	// (NODE_ID, default: hostname).
	NodeID string
	// LogLevel is one of debug, info, warn, error (LOG_LEVEL, default info).
	LogLevel slog.Level
	// Secret signs guest tokens (SECRET). Every node must share it. If unset,
	// the node makes a random one, and guest tokens stop working on restart.
	Secret string
	// MaxConnsPerIP caps open WebSockets per client IP (MAX_CONNS_PER_IP,
	// default 64; 0 means no limit, for load tests from one machine).
	MaxConnsPerIP int
	// Tick is how often each board commits and sends frames (TICK, default
	// 20ms). Longer ticks mean fewer, larger frames per client.
	Tick time.Duration
	// TickMax is the tick of a very busy board (TICK_MAX, default 50ms); a
	// value no longer than TICK keeps every board at TICK.
	TickMax time.Duration
	// TrustProxy takes the client IP from the last X-Forwarded-For entry,
	// appended by the reverse proxy (TRUST_PROXY=true). Set it only when the
	// node is reachable solely through that proxy.
	TrustProxy bool
}

// FromEnv loads the config from the process environment.
func FromEnv() (Config, error) {
	return Load(os.Getenv, os.Hostname)
}

// Load builds a Config from getenv; hostname supplies the default node id.
func Load(getenv func(string) string, hostname func() (string, error)) (Config, error) {
	cfg := Config{
		Addr:        getenv("ADDR"),
		DatabaseURL: getenv("DATABASE_URL"),
		NodeID:      getenv("NODE_ID"),
		Secret:      getenv("SECRET"),
	}
	if cfg.Addr == "" {
		cfg.Addr = ":8081"
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	if cfg.NodeID == "" {
		h, err := hostname()
		if err != nil {
			return Config{}, fmt.Errorf("NODE_ID unset and hostname unavailable: %w", err)
		}
		cfg.NodeID = h
	}
	cfg.MaxConnsPerIP = 64
	if v := getenv("MAX_CONNS_PER_IP"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return Config{}, fmt.Errorf("invalid MAX_CONNS_PER_IP %q", v)
		}
		cfg.MaxConnsPerIP = n
	}
	if v := getenv("TRUST_PROXY"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("invalid TRUST_PROXY %q", v)
		}
		cfg.TrustProxy = b
	}
	if v := getenv("TICK"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Millisecond {
			return Config{}, fmt.Errorf("invalid TICK %q", v)
		}
		cfg.Tick = d
	}
	if v := getenv("TICK_MAX"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Millisecond {
			return Config{}, fmt.Errorf("invalid TICK_MAX %q", v)
		}
		cfg.TickMax = d
	}
	if lvl := getenv("LOG_LEVEL"); lvl != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(strings.ToLower(lvl))); err != nil {
			return Config{}, fmt.Errorf("invalid LOG_LEVEL %q: %w", lvl, err)
		}
	}
	return cfg, nil
}
