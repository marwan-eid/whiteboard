// Package metrics defines the node's Prometheus metrics on a private registry,
// so tests can create isolated instances.
package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

type Metrics struct {
	Registry *prometheus.Registry

	WSConnections prometheus.Gauge
	// WSMessagesIn counts decoded client messages by type
	// (hello, time_ping, op_batch, invalid).
	WSMessagesIn *prometheus.CounterVec
	// Throttled counts client messages delayed by a per-connection rate limit, by limit.
	Throttled *prometheus.CounterVec
	// LimitRejections counts requests refused by a per-IP limit, by limit.
	LimitRejections *prometheus.CounterVec

	BoardsActive    prometheus.Gauge
	BatchesApplied  prometheus.Counter
	BatchesRejected prometheus.Counter
	StampsClamped   prometheus.Counter
	// ClientsKicked counts connections the server closed, by reason.
	ClientsKicked *prometheus.CounterVec
	TickDuration  prometheus.Histogram

	CommitDuration    prometheus.Histogram
	BoardLoadDuration prometheus.Histogram
	SnapshotDuration  prometheus.Histogram
	SnapshotsWritten  prometheus.Counter
	SnapshotFailures  prometheus.Counter
	// BoardFailures counts boards dropped after an I/O error, by stage (load, commit).
	BoardFailures *prometheus.CounterVec
	// FanoutBytes counts frame bytes queued to clients.
	FanoutBytes prometheus.Counter
	// Restores counts point-in-time restores applied.
	Restores prometheus.Counter
	// SyncServerLatency is from a batch arriving until its frames are queued
	// (includes waiting for the tick and the commit).
	SyncServerLatency prometheus.Histogram
	// Live feeds the public stats panel.
	Live *Live
}

func New() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		Registry: reg,
		Live:     newLive(time.Now),
		WSConnections: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ws_connections",
			Help: "Open WebSocket connections on this node.",
		}),
		WSMessagesIn: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ws_messages_in_total",
			Help: "Client messages received, by message type.",
		}, []string{"type"}),
		Throttled: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ws_throttled_total",
			Help: "Client messages delayed by a per-connection rate limit, by limit (batches, ops, bytes).",
		}, []string{"limit"}),
		LimitRejections: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "limit_rejections_total",
			Help: "Requests refused by a per-IP limit, by limit (connections, board_creates).",
		}, []string{"limit"}),
		BoardsActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "boards_active",
			Help: "Boards loaded on this node.",
		}),
		BatchesApplied: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "batches_applied_total",
			Help: "Client op batches validated, sequenced and applied.",
		}),
		BatchesRejected: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "batches_rejected_total",
			Help: "Client op batches rejected by validation or ordering checks.",
		}),
		StampsClamped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "stamps_clamped_total",
			Help: "Batches whose stamp was too far ahead of server time and was replaced.",
		}),
		ClientsKicked: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "clients_kicked_total",
			Help: "Connections closed by the server, by reason.",
		}, []string{"reason"}),
		TickDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "board_tick_duration_seconds",
			Help:    "Time a board spends building and queueing one tick's frames.",
			Buckets: prometheus.ExponentialBuckets(0.00005, 2, 14), // 50µs .. ~400ms
		}),
		CommitDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "board_commit_duration_seconds",
			Help:    "Time to durably append one tick's batches to the log.",
			Buckets: prometheus.ExponentialBuckets(0.0001, 2, 14), // 100µs .. ~800ms
		}),
		BoardLoadDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "board_load_duration_seconds",
			Help:    "Time to load a board from its snapshot and log tail.",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 14), // 1ms .. ~8s
		}),
		SnapshotDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "board_snapshot_duration_seconds",
			Help:    "Time to compress and write one snapshot.",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 14),
		}),
		SnapshotsWritten: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "board_snapshots_written_total",
			Help: "Snapshots written.",
		}),
		SnapshotFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "board_snapshot_failures_total",
			Help: "Snapshot writes that failed (the log still has every batch).",
		}),
		BoardFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "board_failures_total",
			Help: "Boards dropped after an I/O failure, by stage.",
		}, []string{"stage"}),
		FanoutBytes: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "fanout_bytes_total",
			Help: "Frame bytes queued to clients.",
		}),
		Restores: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "board_restores_total",
			Help: "Point-in-time restores applied.",
		}),
		SyncServerLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "sync_server_latency_seconds",
			Help:    "From a batch arriving until its frames are queued to other clients.",
			Buckets: prometheus.ExponentialBuckets(0.001, 1.5, 20), // 1ms .. ~2.2s
		}),
	}
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.WSConnections, m.WSMessagesIn, m.Throttled, m.LimitRejections,
		m.BoardsActive, m.BatchesApplied, m.BatchesRejected, m.StampsClamped,
		m.ClientsKicked, m.TickDuration,
		m.CommitDuration, m.BoardLoadDuration, m.SnapshotDuration,
		m.SnapshotsWritten, m.SnapshotFailures, m.BoardFailures,
		m.FanoutBytes, m.SyncServerLatency, m.Restores,
	)
	return m
}
