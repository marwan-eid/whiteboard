package config

import (
	"errors"
	"log/slog"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func host(name string) func() (string, error) {
	return func() (string, error) { return name, nil }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(map[string]string{"DATABASE_URL": "postgres://x"}), host("box-1"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":8081" || cfg.NodeID != "box-1" || cfg.LogLevel != slog.LevelInfo || cfg.MaxConnsPerIP != 64 || cfg.TrustProxy {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"DATABASE_URL":     "postgres://x",
		"ADDR":             ":9000",
		"NODE_ID":          "node-7",
		"LOG_LEVEL":        "DEBUG",
		"MAX_CONNS_PER_IP": "0",
		"TRUST_PROXY":      "true",
	}), host("ignored"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":9000" || cfg.NodeID != "node-7" || cfg.LogLevel != slog.LevelDebug || cfg.MaxConnsPerIP != 0 || !cfg.TrustProxy {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
}

func TestLoadErrors(t *testing.T) {
	failingHost := func() (string, error) { return "", errors.New("no hostname") }
	cases := map[string]struct {
		env  map[string]string
		host func() (string, error)
	}{
		"missing DATABASE_URL": {map[string]string{}, host("h")},
		"bad LOG_LEVEL":        {map[string]string{"DATABASE_URL": "x", "LOG_LEVEL": "loud"}, host("h")},
		"no node id":           {map[string]string{"DATABASE_URL": "x"}, failingHost},
		"bad MAX_CONNS_PER_IP": {map[string]string{"DATABASE_URL": "x", "MAX_CONNS_PER_IP": "-1"}, host("h")},
		"bad TRUST_PROXY":      {map[string]string{"DATABASE_URL": "x", "TRUST_PROXY": "maybe"}, host("h")},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(env(c.env), c.host); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
