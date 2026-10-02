// Package httpapi wires the node's HTTP endpoints.
package httpapi

import (
	"context"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"whiteboard/internal/access"
	"whiteboard/internal/metrics"
	"whiteboard/internal/protocol"
	"whiteboard/internal/ratelimit"
	"whiteboard/internal/usage"
)

type Deps struct {
	// Gateway serves WebSocket upgrades on /ws.
	Gateway http.Handler
	Metrics *metrics.Metrics
	// Ready reports whether the node can serve traffic (e.g. Postgres reachable).
	Ready func(context.Context) error
	// Signer and Boards enable the /api/guest and /api/boards endpoints.
	Signer *access.Signer
	Boards Boards
	// KickLink disconnects a revoked link's live users.
	KickLink func(boardID, linkID string)
	// BoardCreates limits POST /api/boards per client IP (nil: no limit).
	BoardCreates *ratelimit.Keyed
	// TrustProxy takes the client IP from X-Forwarded-For (see config.TrustProxy).
	TrustProxy bool
	// NodeID is this node, reported by /api/stats and by the route API when
	// Route is nil (a single node).
	NodeID string
	// Route names the node serving a board (cluster.Node.Route).
	Route func(ctx context.Context, boardID string) (string, error)
	// Usage reports usage counts for /api/stats (usage.Recorder.Counts).
	Usage func(ctx context.Context) (usage.Counts, error)
}

func NewRouter(d Deps) http.Handler {
	mux := http.NewServeMux()

	// Liveness: the process is up. Used by container health checks.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})

	// Readiness: dependencies are reachable.
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := d.Ready(ctx); err != nil {
			http.Error(w, "not ready: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ready\n"))
	})

	// Public live numbers for the stats panel.
	(&statsAPI{node: d.NodeID, m: d.Metrics, every: time.Second, usage: d.Usage}).register(mux)

	// Scraped by Prometheus on the internal network; Caddy does not expose it.
	mux.Handle("GET /metrics", promhttp.HandlerFor(d.Metrics.Registry, promhttp.HandlerOpts{}))

	// Profiling, on the internal network only (like /metrics).
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)

	if d.Signer != nil && d.Boards != nil {
		(&boardAPI{
			signer: d.Signer, boards: d.Boards, kickLink: d.KickLink,
			creates: d.BoardCreates, trustProxy: d.TrustProxy, metrics: d.Metrics,
		}).register(mux)
	}

	mux.Handle("GET /ws", d.Gateway)
	// /n/{node}/ws is how clients reach a given node through Caddy. The node
	// does not trust the path: it serves the board only if it holds its lease,
	// and otherwise answers Moved.
	mux.Handle("GET /n/{node}/ws", d.Gateway)

	// Which node serves a board, for clients to connect to it directly.
	mux.HandleFunc("GET /api/boards/{board}/route", func(w http.ResponseWriter, r *http.Request) {
		boardID := r.PathValue("board")
		if !protocol.ValidBoardID(boardID) {
			http.Error(w, "invalid board id", http.StatusBadRequest)
			return
		}
		node := d.NodeID
		if d.Route != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			n, err := d.Route(ctx, boardID)
			if err != nil {
				http.Error(w, "routing unavailable", http.StatusServiceUnavailable)
				return
			}
			node = n
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]string{"nodeId": node})
	})
	return mux
}
