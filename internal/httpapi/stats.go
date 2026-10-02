package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"whiteboard/internal/metrics"
)

// Stats is what the public live stats panel shows, for this node. Every
// number says what it measures; see metrics.LiveStats.
type Stats struct {
	Node string `json:"node"`
	// Connections is open WebSockets on this node.
	Connections int `json:"connections"`
	// Boards is boards loaded on this node.
	Boards int `json:"boards"`
	metrics.LiveStats
}

// statsAPI serves the stats as JSON and as a server-sent event stream, one
// event a second. The snapshot is shared, so many viewers cost one
// computation a second.
type statsAPI struct {
	node  string
	m     *metrics.Metrics
	every time.Duration

	mu   sync.Mutex
	at   time.Time
	last Stats
}

func (a *statsAPI) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/stats", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, a.snapshot())
	})
	mux.HandleFunc("GET /api/stats/stream", a.stream)
	// The same, for one node through Caddy (the panel shows the browser's node).
	mux.HandleFunc("GET /n/{node}/api/stats/stream", a.stream)
}

func (a *statsAPI) snapshot() Stats {
	a.mu.Lock()
	defer a.mu.Unlock()
	if time.Since(a.at) >= a.every/2 {
		a.at = time.Now()
		a.last = Stats{
			Node:        a.node,
			Connections: int(metrics.GaugeValue(a.m.WSConnections)),
			Boards:      int(metrics.GaugeValue(a.m.BoardsActive)),
			LiveStats:   a.m.Live.Snapshot(),
		}
	}
	return a.last
}

func (a *statsAPI) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = fmt.Fprint(w, "retry: 5000\n\n")
	t := time.NewTicker(a.every)
	defer t.Stop()
	for {
		data, err := json.Marshal(a.snapshot())
		if err != nil {
			return
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return
		}
		flusher.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-t.C:
		}
	}
}
