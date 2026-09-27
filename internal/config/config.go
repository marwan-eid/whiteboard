// Package config loads node configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
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
	if lvl := getenv("LOG_LEVEL"); lvl != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(strings.ToLower(lvl))); err != nil {
			return Config{}, fmt.Errorf("invalid LOG_LEVEL %q: %w", lvl, err)
		}
	}
	return cfg, nil
}
