// Package cluster runs several nodes on one Postgres (ADR-0005): node
// membership by heartbeat, board placement by rendezvous hashing over the
// live nodes, and board leases with epochs. Fencing is the store's: a node
// that lost a board without noticing fails its next commit on the
// (board_id, seq) primary key and drops the board.
package cluster

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"whiteboard/internal/board"
)

// Config sets the timings; zero values take the defaults from ADR-0005.
type Config struct {
	NodeID string
	Addr   string
	// Heartbeat is how often the node reports itself; a node is live while
	// its last heartbeat is younger than LiveFor.
	Heartbeat time.Duration
	LiveFor   time.Duration
	// LeaseTTL is how long a lease lasts without renewal. The board registry
	// renews every LeaseTTL*2/5 (2 s of 5 s).
	LeaseTTL time.Duration
}

func (c *Config) setDefaults() {
	if c.Heartbeat == 0 {
		c.Heartbeat = time.Second
	}
	if c.LiveFor == 0 {
		c.LiveFor = 3 * c.Heartbeat
	}
	if c.LeaseTTL == 0 {
		c.LeaseTTL = 5 * time.Second
	}
}

// Node is this process's view of the cluster. It implements board.Placement.
type Node struct {
	cfg  Config
	pool *pgxpool.Pool
	log  *slog.Logger

	mu   sync.Mutex
	live []string // sorted; refreshed every heartbeat
}

var _ board.Placement = (*Node)(nil)

func New(cfg Config, pool *pgxpool.Pool, log *slog.Logger) *Node {
	cfg.setDefaults()
	return &Node{cfg: cfg, pool: pool, log: log, live: []string{cfg.NodeID}}
}

func (n *Node) ID() string { return n.cfg.NodeID }

// RenewEvery is how often the board registry should renew its leases.
func (n *Node) RenewEvery() time.Duration { return n.cfg.LeaseTTL * 2 / 5 }

// LeaseTTL is how long a lease lasts without renewal.
func (n *Node) LeaseTTL() time.Duration { return n.cfg.LeaseTTL }

// Join announces the node and loads the live set; call it before serving.
func (n *Node) Join(ctx context.Context) error { return n.heartbeat(ctx) }

// Run heartbeats until ctx ends. Call Leave after it on a clean shutdown.
func (n *Node) Run(ctx context.Context) {
	t := time.NewTicker(n.cfg.Heartbeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			hbCtx, cancel := context.WithTimeout(ctx, n.cfg.Heartbeat)
			if err := n.heartbeat(hbCtx); err != nil && ctx.Err() == nil {
				n.log.Warn("heartbeat failed", "err", err)
			}
			cancel()
		}
	}
}

// Leave removes the node from membership, so other nodes rebalance at once
// rather than after LiveFor.
func (n *Node) Leave() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := n.pool.Exec(ctx, "DELETE FROM nodes WHERE id = $1", n.cfg.NodeID); err != nil {
		n.log.Warn("leaving the cluster failed", "err", err)
	}
}

func interval(d time.Duration) string { return fmt.Sprintf("%d milliseconds", d.Milliseconds()) }

func (n *Node) heartbeat(ctx context.Context) error {
	if _, err := n.pool.Exec(ctx, `
		INSERT INTO nodes (id, addr, heartbeat_at) VALUES ($1, $2, now())
		ON CONFLICT (id) DO UPDATE SET addr = $2, heartbeat_at = now()`, n.cfg.NodeID, n.cfg.Addr); err != nil {
		return err
	}
	rows, err := n.pool.Query(ctx, "SELECT id FROM nodes WHERE heartbeat_at > now() - $1::interval ORDER BY id",
		interval(n.cfg.LiveFor))
	if err != nil {
		return err
	}
	live, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if !slices.Contains(live, n.cfg.NodeID) {
		live = append(live, n.cfg.NodeID)
		slices.Sort(live)
	}
	n.mu.Lock()
	n.live = live
	n.mu.Unlock()
	return nil
}

// Live returns the live node ids, sorted, as of the last heartbeat.
func (n *Node) Live() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.live)
}

// Preferred is the rendezvous (highest random weight) choice of node for a
// board: when a node joins or leaves, only the boards it wins or held move.
func Preferred(boardID string, nodes []string) string {
	var best string
	var bestScore uint64
	for _, id := range nodes {
		h := fnv.New64a()
		_, _ = h.Write([]byte(id))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(boardID))
		if s := mix(h.Sum64()); best == "" || s > bestScore || (s == bestScore && id < best) {
			best, bestScore = id, s
		}
	}
	return best
}

// mix is splitmix64's finalizer: FNV alone spreads similar keys poorly.
func mix(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	return x ^ x>>31
}

// holder returns the node holding an unexpired lease on the board, if any.
func (n *Node) holder(ctx context.Context, boardID string) (string, error) {
	var node string
	err := n.pool.QueryRow(ctx,
		"SELECT node_id FROM board_leases WHERE board_id = $1 AND expires_at > now()", boardID).Scan(&node)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return node, err
}

// Route names the node that serves (or should serve) the board: the lease
// holder while its lease lasts, else the preferred live node.
func (n *Node) Route(ctx context.Context, boardID string) (string, error) {
	h, err := n.holder(ctx, boardID)
	if err != nil || h != "" {
		return h, err
	}
	return Preferred(boardID, n.Live()), nil
}

// Claim takes the board's lease for this node, or says which node should
// serve it instead (*board.MovedError).
func (n *Node) Claim(ctx context.Context, boardID string) (uint64, error) {
	h, err := n.holder(ctx, boardID)
	if err != nil {
		return 0, err
	}
	switch {
	case h != "" && h != n.cfg.NodeID:
		return 0, &board.MovedError{Node: h}
	case h == "":
		if p := Preferred(boardID, n.Live()); p != n.cfg.NodeID {
			return 0, &board.MovedError{Node: p}
		}
	}
	var epoch int64
	err = n.pool.QueryRow(ctx, `
		INSERT INTO board_leases (board_id, node_id, epoch, expires_at)
		SELECT $1, $2, 1, now() + $3::interval
		-- A node the others consider dead (no recent heartbeat) takes nothing.
		WHERE EXISTS (SELECT 1 FROM nodes WHERE id = $2 AND heartbeat_at > now() - $4::interval)
		ON CONFLICT (board_id) DO UPDATE SET node_id = $2, epoch = board_leases.epoch + 1, expires_at = now() + $3::interval
			WHERE board_leases.expires_at <= now() OR board_leases.node_id = $2
		RETURNING epoch`, boardID, n.cfg.NodeID, interval(n.cfg.LeaseTTL), interval(n.cfg.LiveFor)).Scan(&epoch)
	if errors.Is(err, pgx.ErrNoRows) {
		// Another node took it between our read and our write.
		if h, herr := n.holder(ctx, boardID); herr == nil && h != "" {
			return 0, &board.MovedError{Node: h}
		}
		return 0, errors.New("board lease is changing hands, or this node is not live; retry")
	}
	return uint64(epoch), err
}

// Renew extends the leases this node holds at the given epochs, in one
// statement, and returns the boards whose lease it no longer holds.
func (n *Node) Renew(ctx context.Context, leases map[string]uint64) ([]string, error) {
	if len(leases) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(leases))
	epochs := make([]int64, 0, len(leases))
	for id, e := range leases {
		ids = append(ids, id)
		epochs = append(epochs, int64(e))
	}
	rows, err := n.pool.Query(ctx, `
		UPDATE board_leases l SET expires_at = now() + $4::interval
		FROM unnest($2::text[], $3::bigint[]) AS mine(board_id, epoch)
		WHERE l.board_id = mine.board_id AND l.epoch = mine.epoch AND l.node_id = $1 AND l.expires_at > now()
			AND EXISTS (SELECT 1 FROM nodes WHERE id = $1 AND heartbeat_at > now() - $5::interval)
		RETURNING l.board_id`, n.cfg.NodeID, ids, epochs, interval(n.cfg.LeaseTTL), interval(n.cfg.LiveFor))
	if err != nil {
		return nil, err
	}
	kept, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	var lost []string
	for _, id := range ids {
		if !slices.Contains(kept, id) {
			lost = append(lost, id)
		}
	}
	return lost, nil
}

// Release ends a lease at once (when a board unloads), so another node can
// take the board without waiting for it to expire.
func (n *Node) Release(ctx context.Context, boardID string, epoch uint64) error {
	_, err := n.pool.Exec(ctx,
		"UPDATE board_leases SET expires_at = now() WHERE board_id = $1 AND node_id = $2 AND epoch = $3",
		boardID, n.cfg.NodeID, int64(epoch))
	return err
}
