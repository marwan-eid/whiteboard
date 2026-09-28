// Package ratelimit holds the per-IP limits (docs/ARCHITECTURE.md, "Auth,
// permissions, abuse"). Per-connection limits use golang.org/x/time/rate
// directly.
package ratelimit

import (
	"net"
	"net/http"
	"strings"
	"sync"

	"golang.org/x/time/rate"
)

// maxKeys bounds memory: past it, idle entries are dropped.
const maxKeys = 10_000

// Keyed is a token bucket per key (an IP address).
type Keyed struct {
	mu    sync.Mutex
	rate  rate.Limit
	burst int
	m     map[string]*rate.Limiter
}

// NewKeyed allows each key `burst` events at once, refilled at `r` per second.
func NewKeyed(r rate.Limit, burst int) *Keyed {
	return &Keyed{rate: r, burst: burst, m: make(map[string]*rate.Limiter)}
}

// Allow takes one token from key's bucket, reporting whether there was one.
func (k *Keyed) Allow(key string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	l, ok := k.m[key]
	if !ok {
		if len(k.m) >= maxKeys {
			k.dropFull()
		}
		l = rate.NewLimiter(k.rate, k.burst)
		k.m[key] = l
	}
	return l.Allow()
}

// dropFull forgets keys whose bucket has refilled: they are as good as new.
func (k *Keyed) dropFull() {
	for key, l := range k.m {
		if l.Tokens() >= float64(k.burst) {
			delete(k.m, key)
		}
	}
}

// Counter caps concurrent uses per key (open WebSockets per IP).
type Counter struct {
	mu  sync.Mutex
	max int
	m   map[string]int
}

// NewCounter allows max concurrent uses per key; 0 means no limit.
func NewCounter(max int) *Counter { return &Counter{max: max, m: make(map[string]int)} }

// Acquire takes a slot for key; call Release when done if it returns true.
func (c *Counter) Acquire(key string) bool {
	if c.max <= 0 {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m[key] >= c.max {
		return false
	}
	c.m[key]++
	return true
}

func (c *Counter) Release(key string) {
	if c.max <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m[key]--; c.m[key] <= 0 {
		delete(c.m, key)
	}
}

// ClientIP returns the address a request came from. Behind a trusted reverse
// proxy (Caddy) that is the last X-Forwarded-For entry, which the proxy
// itself appended; otherwise the TCP peer.
func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
			last := xff[len(xff)-1]
			if i := strings.LastIndexByte(last, ','); i >= 0 {
				last = last[i+1:]
			}
			if ip := strings.TrimSpace(last); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
