package ratelimit

import (
	"net/http/httptest"
	"slices"
	"testing"
)

func TestKeyed(t *testing.T) {
	k := NewKeyed(0, 2) // no refill
	if got := []bool{k.Allow("a"), k.Allow("a"), k.Allow("a")}; !slices.Equal(got, []bool{true, true, false}) {
		t.Fatal("key a should get exactly its burst")
	}
	if !k.Allow("b") {
		t.Fatal("keys must not share a bucket")
	}
}

func TestCounter(t *testing.T) {
	c := NewCounter(2)
	if got := []bool{c.Acquire("a"), c.Acquire("a"), c.Acquire("a")}; !slices.Equal(got, []bool{true, true, false}) {
		t.Fatal("want exactly 2 slots")
	}
	c.Release("a")
	if !c.Acquire("a") {
		t.Fatal("a released slot should be reusable")
	}
	if len(c.m) != 1 {
		t.Fatalf("map = %v", c.m)
	}
	c.Release("a")
	c.Release("a")
	if len(c.m) != 0 {
		t.Fatalf("idle keys must be forgotten: %v", c.m)
	}
	unlimited := NewCounter(0)
	for range 100 {
		if !unlimited.Acquire("a") {
			t.Fatal("0 means unlimited")
		}
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.2:5555"
	r.Header.Add("X-Forwarded-For", "6.6.6.6, 1.2.3.4")
	if got := ClientIP(r, false); got != "10.0.0.2" {
		t.Fatalf("untrusted: %q", got)
	}
	// A client-supplied X-Forwarded-For comes first; the proxy appends the real address.
	if got := ClientIP(r, true); got != "1.2.3.4" {
		t.Fatalf("trusted: %q", got)
	}
}
