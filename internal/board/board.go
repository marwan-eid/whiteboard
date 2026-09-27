// Package board runs one actor goroutine per live board. The actor is the
// board's single writer: it validates client batches, assigns each a seq and
// merges it into the document. Once per tick it durably appends that tick's
// batches to the log, then sends every client one frame with other clients'
// batches and acks for its own: nothing is acked or shown to others before
// it is committed.
//
// Failure handling is crash-only: if the board cannot commit, it drops every
// client and unloads. Clients keep unacked edits and resend them to the
// reloaded board, which has exactly the committed state.
package board

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"google.golang.org/protobuf/proto"

	"whiteboard/internal/doc"
	"whiteboard/internal/hlc"
	"whiteboard/internal/metrics"
	pb "whiteboard/internal/pb/whiteboard/v1"
	"whiteboard/internal/protocol"
)

// errClosed means the board unloaded normally; callers get a fresh one from the Registry.
var errClosed = errors.New("board closed")

// KickReason says why the board dropped a connection.
type KickReason int

const (
	// KickSlow: the client could not keep up; it should reconnect and resync.
	KickSlow KickReason = iota
	// KickReplaced: a newer connection with the same client id joined; the
	// old one must not reconnect, or the two would evict each other forever.
	KickReplaced
	// KickReload: the board failed or is shutting down; reconnect and resync.
	KickReload
)

// Conn is the board's handle on a client connection.
type Conn interface {
	ClientID() uint64
	// Send queues an encoded ServerMessage without blocking; false means the
	// client's queue is full.
	Send(msg []byte) bool
	// Kick closes the connection.
	Kick(reason KickReason)
}

type Config struct {
	NodeID string
	Store  Store
	// Tick is how often accepted batches are committed and broadcast.
	Tick time.Duration
	// MaxSkew is how far ahead of server time a client stamp may be before
	// the server replaces it with its own clock.
	MaxSkew time.Duration
	// IdleTimeout unloads a board that has had no clients for this long.
	IdleTimeout time.Duration
	// SnapshotEvery writes a snapshot after this many new batches, or after
	// SnapshotInterval if anything changed, whichever comes first.
	SnapshotEvery    uint64
	SnapshotInterval time.Duration
	// IOTimeout bounds each load, commit and snapshot write.
	IOTimeout time.Duration
	Now       func() time.Time
}

func (c *Config) setDefaults() {
	if c.Tick == 0 {
		c.Tick = 20 * time.Millisecond
	}
	if c.MaxSkew == 0 {
		c.MaxSkew = 2 * time.Second
	}
	if c.IdleTimeout == 0 {
		c.IdleTimeout = 10 * time.Minute
	}
	if c.SnapshotEvery == 0 {
		c.SnapshotEvery = 5000
	}
	if c.SnapshotInterval == 0 {
		c.SnapshotInterval = 10 * time.Minute
	}
	if c.IOTimeout == 0 {
		c.IOTimeout = 10 * time.Second
	}
	if c.Now == nil {
		c.Now = time.Now
	}
}

type Board struct {
	id      string
	cfg     Config
	log     *slog.Logger
	metrics *metrics.Metrics
	inbox   chan any
	done    chan struct{}
	// err is why the board closed abnormally; read it only after done is closed.
	err error
	// onClose removes the board from its registry; it runs on the actor goroutine.
	onClose func(*Board)

	// Owned by the actor goroutine.
	doc             *doc.Doc
	seq             uint64
	clock           *hlc.Clock
	lastClientSeq   map[uint64]uint64
	clients         map[uint64]Conn
	joining         []Conn
	uncommitted     []LogEntry
	outAcks         map[uint64][]*pb.Ack
	idleSince       time.Time
	lastSnapshotSeq uint64
	lastSnapshotAt  time.Time
	snapshotting    chan struct{} // closed when the in-flight snapshot write finishes
}

type joinMsg struct {
	conn  Conn
	reply chan error
}

type leaveMsg struct{ conn Conn }

type batchMsg struct {
	clientID uint64
	batch    *pb.OpBatch
}

type snapshotMsg struct {
	reply chan Snapshot
}

type closeMsg struct {
	reply chan error
}

type crashMsg struct{}

// Snapshot is the board state at a point in its history.
type Snapshot struct {
	Seq     uint64
	Objects []*pb.ObjectState
}

func newBoard(id string, cfg Config, log *slog.Logger, m *metrics.Metrics, onClose func(*Board)) *Board {
	b := &Board{
		id:             id,
		cfg:            cfg,
		log:            log.With("board", id),
		metrics:        m,
		inbox:          make(chan any, 1024),
		done:           make(chan struct{}),
		onClose:        onClose,
		doc:            doc.New(),
		clock:          hlc.NewClock(0, func() int64 { return cfg.Now().UnixMilli() }),
		lastClientSeq:  map[uint64]uint64{},
		clients:        map[uint64]Conn{},
		outAcks:        map[uint64][]*pb.Ack{},
		idleSince:      cfg.Now(),
		lastSnapshotAt: cfg.Now(),
	}
	go b.run()
	return b
}

func (b *Board) ID() string { return b.id }

// closedErr is what callers see after the board's done channel closes.
func (b *Board) closedErr() error {
	if b.err != nil {
		return b.err
	}
	return errClosed
}

// send delivers a message to the actor, blocking while its inbox is full so
// that a fast client is slowed down rather than dropped.
func (b *Board) send(ctx context.Context, m any) error {
	select {
	case b.inbox <- m:
		return nil
	case <-b.done:
		return b.closedErr()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *Board) join(ctx context.Context, c Conn) error {
	reply := make(chan error, 1)
	if err := b.send(ctx, joinMsg{conn: c, reply: reply}); err != nil {
		return err
	}
	select {
	case err := <-reply:
		return err
	case <-b.done:
		return b.closedErr()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Leave detaches a connection. It never blocks on a closed board.
func (b *Board) Leave(c Conn) {
	_ = b.send(context.Background(), leaveMsg{conn: c})
}

// Submit hands a client batch to the board. The result arrives as an Ack in a later frame.
func (b *Board) Submit(ctx context.Context, clientID uint64, batch *pb.OpBatch) error {
	return b.send(ctx, batchMsg{clientID: clientID, batch: batch})
}

// Snapshot returns the current state, including batches not yet committed.
func (b *Board) Snapshot(ctx context.Context) (Snapshot, error) {
	reply := make(chan Snapshot, 1)
	if err := b.send(ctx, snapshotMsg{reply: reply}); err != nil {
		return Snapshot{}, err
	}
	select {
	case s := <-reply:
		return s, nil
	case <-b.done:
		return Snapshot{}, b.closedErr()
	case <-ctx.Done():
		return Snapshot{}, ctx.Err()
	}
}

// close commits what's pending, writes a final snapshot and unloads.
func (b *Board) close(ctx context.Context) error {
	reply := make(chan error, 1)
	if err := b.send(ctx, closeMsg{reply: reply}); err != nil {
		if errors.Is(err, errClosed) {
			return nil
		}
		return err
	}
	select {
	case err := <-reply:
		return err
	case <-b.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *Board) run() {
	b.metrics.BoardsActive.Inc()
	defer b.metrics.BoardsActive.Dec()

	if err := b.load(); err != nil {
		b.fail("load", err)
		return
	}
	ticker := time.NewTicker(b.cfg.Tick)
	defer ticker.Stop()
	for {
		select {
		case m := <-b.inbox:
			switch m := m.(type) {
			case closeMsg:
				m.reply <- b.shutdown()
				return
			case crashMsg:
				b.fail("crash", errors.New("crashed on purpose"))
				return
			default:
				b.handle(m)
			}
		case <-ticker.C:
			if err := b.flush(); err != nil {
				b.fail("commit", err)
				return
			}
			b.maybeSnapshot()
			if len(b.clients) == 0 && b.cfg.Now().Sub(b.idleSince) >= b.cfg.IdleTimeout {
				if err := b.shutdown(); err != nil {
					b.log.Warn("final snapshot failed", "err", err)
				}
				return
			}
		}
	}
}

// load rebuilds the board from its latest snapshot plus the log after it.
func (b *Board) load() error {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), b.cfg.IOTimeout)
	defer cancel()
	loaded, err := b.cfg.Store.Load(ctx, b.id)
	if err != nil {
		return err
	}
	if s := loaded.Snapshot; s != nil {
		d, err := doc.FromSnapshot(s.GetObjects())
		if err != nil {
			return fmt.Errorf("snapshot at seq %d: %w", s.GetSeq(), err)
		}
		b.doc, b.seq, b.lastSnapshotSeq = d, s.GetSeq(), s.GetSeq()
		for _, c := range s.GetClients() {
			b.lastClientSeq[c.GetClientId()] = c.GetLastClientSeq()
		}
		for _, o := range s.GetObjects() {
			for _, fs := range o.GetStamps() {
				b.clock.Observe(hlc.FromProto(fs.GetStamp()))
			}
		}
	}
	for _, e := range loaded.Tail {
		if e.Seq != b.seq+1 {
			return fmt.Errorf("log gap: expected seq %d, found %d", b.seq+1, e.Seq)
		}
		st := hlc.FromProto(e.Stamp)
		b.doc.ApplyBatch(e.Ops, st)
		b.clock.Observe(st)
		b.seq = e.Seq
		b.lastClientSeq[e.ClientID] = max(b.lastClientSeq[e.ClientID], e.ClientSeq)
	}
	b.metrics.BoardLoadDuration.Observe(time.Since(start).Seconds())
	b.log.Info("board loaded", "seq", b.seq, "objects", b.doc.Len(), "replayed", len(loaded.Tail), "took", time.Since(start))
	return nil
}

func (b *Board) handle(m any) {
	switch m := m.(type) {
	case joinMsg:
		// A reconnecting client often arrives before the server has noticed its
		// previous socket died (half-open TCP can linger for minutes), so the
		// newest connection for a client id wins.
		id := m.conn.ClientID()
		if old, taken := b.clients[id]; taken {
			b.metrics.ClientsKicked.WithLabelValues("replaced").Inc()
			old.Kick(KickReplaced)
			b.remove(old)
		}
		b.clients[id] = m.conn
		b.joining = append(b.joining, m.conn)
		m.reply <- nil
	case leaveMsg:
		b.remove(m.conn)
	case batchMsg:
		b.apply(m.clientID, m.batch)
	case snapshotMsg:
		m.reply <- Snapshot{Seq: b.seq, Objects: b.doc.Snapshot()}
	}
}

func (b *Board) remove(c Conn) {
	id := c.ClientID()
	if b.clients[id] != c {
		return
	}
	delete(b.clients, id)
	delete(b.outAcks, id)
	for i, j := range b.joining {
		if j == c {
			b.joining = append(b.joining[:i], b.joining[i+1:]...)
			break
		}
	}
	if len(b.clients) == 0 {
		b.idleSince = b.cfg.Now()
	}
}

func (b *Board) apply(clientID uint64, batch *pb.OpBatch) {
	if _, ok := b.clients[clientID]; !ok {
		return // left before its batch was processed
	}
	cs := batch.GetClientSeq()
	last := b.lastClientSeq[clientID]
	switch {
	case cs <= last:
		// Already applied (a resend after reconnect). Ack again without reapplying.
		b.ack(clientID, &pb.Ack{ClientSeq: cs})
		return
	case cs != last+1:
		b.reject(clientID, cs, "client_seq out of order")
		return
	}
	b.lastClientSeq[clientID] = cs

	if err := b.doc.ValidateBatch(batch.GetOps(), clientID); err != nil {
		b.reject(clientID, cs, err.Error())
		return
	}

	st := hlc.FromProto(batch.GetStamp())
	st.ClientID = clientID
	if limit := b.cfg.Now().Add(b.cfg.MaxSkew).UnixMilli(); st.WallMs > limit {
		clamped := b.clock.Now()
		clamped.ClientID = clientID
		b.metrics.StampsClamped.Inc()
		st = clamped
	}
	b.clock.Observe(st)

	b.doc.ApplyBatch(batch.GetOps(), st)
	b.seq++
	b.metrics.BatchesApplied.Inc()
	b.uncommitted = append(b.uncommitted, LogEntry{
		Seq: b.seq, ClientID: clientID, ClientSeq: cs, Stamp: st.Proto(), Ops: batch.GetOps(),
	})
	b.ack(clientID, &pb.Ack{ClientSeq: cs, Seq: b.seq, Stamp: st.Proto()})
}

func (b *Board) reject(clientID, clientSeq uint64, reason string) {
	b.metrics.BatchesRejected.Inc()
	b.log.Info("rejected batch", "client", clientID, "client_seq", clientSeq, "reason", reason)
	if clientSeq > b.lastClientSeq[clientID] {
		b.lastClientSeq[clientID] = clientSeq
	}
	b.ack(clientID, &pb.Ack{ClientSeq: clientSeq, Rejected: true, Reason: reason})
}

func (b *Board) ack(clientID uint64, a *pb.Ack) {
	b.outAcks[clientID] = append(b.outAcks[clientID], a)
}

// flush commits this tick's batches, then sends frames, then welcomes
// clients that joined, so a joiner's snapshot already contains everything
// the frames carried.
func (b *Board) flush() error {
	start := time.Now()
	var batches []*pb.SequencedBatch
	if len(b.uncommitted) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), b.cfg.IOTimeout)
		err := b.cfg.Store.Append(ctx, b.id, b.uncommitted)
		cancel()
		b.metrics.CommitDuration.Observe(time.Since(start).Seconds())
		if err != nil {
			return err
		}
		batches = make([]*pb.SequencedBatch, len(b.uncommitted))
		for i, e := range b.uncommitted {
			batches[i] = &pb.SequencedBatch{Seq: e.Seq, Stamp: e.Stamp, Ops: e.Ops}
		}
		b.uncommitted = b.uncommitted[:0]
	}

	joining := map[Conn]bool{}
	for _, c := range b.joining {
		joining[c] = true
	}

	if len(batches) > 0 || len(b.outAcks) > 0 {
		for id, c := range b.clients {
			if joining[c] {
				continue
			}
			frame := &pb.Frame{Acks: b.outAcks[id]}
			for _, sb := range batches {
				if sb.GetStamp().GetClientId() != id {
					frame.Batches = append(frame.Batches, sb)
				}
			}
			if len(frame.Batches) > 0 || len(frame.Acks) > 0 {
				b.deliver(c, &pb.ServerMessage{Msg: &pb.ServerMessage_Frame{Frame: frame}})
			}
		}
		clear(b.outAcks)
	}

	if len(b.joining) > 0 {
		objects := b.doc.Snapshot()
		for _, c := range b.joining {
			b.deliver(c, &pb.ServerMessage{Msg: &pb.ServerMessage_Welcome{Welcome: &pb.Welcome{
				ProtocolVersion: protocol.Version,
				NodeId:          b.cfg.NodeID,
				ServerTimeMs:    b.cfg.Now().UnixMilli(),
				Seq:             b.seq,
				Objects:         objects,
				LastClientSeq:   b.lastClientSeq[c.ClientID()],
			}}})
		}
		b.joining = b.joining[:0]
	}
	b.metrics.TickDuration.Observe(time.Since(start).Seconds())
	return nil
}

func (b *Board) deliver(c Conn, msg *pb.ServerMessage) {
	data, err := proto.Marshal(msg)
	if err != nil {
		b.log.Error("marshal server message", "err", err)
		return
	}
	if !c.Send(data) {
		b.metrics.ClientsKicked.WithLabelValues("slow_consumer").Inc()
		c.Kick(KickSlow)
		b.remove(c)
	}
}

// buildSnapshot captures committed state; call it only right after flush.
func (b *Board) buildSnapshot() *pb.BoardSnapshot {
	s := &pb.BoardSnapshot{Seq: b.seq, Objects: b.doc.Snapshot()}
	ids := make([]uint64, 0, len(b.lastClientSeq))
	for id := range b.lastClientSeq {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		s.Clients = append(s.Clients, &pb.ClientProgress{ClientId: id, LastClientSeq: b.lastClientSeq[id]})
	}
	return s
}

// maybeSnapshot starts a background snapshot write once enough batches have
// accumulated. The snapshot is built on the actor goroutine (a copy), so the
// board keeps serving while it is compressed and written.
func (b *Board) maybeSnapshot() {
	if b.snapshotting != nil {
		select {
		case <-b.snapshotting:
			b.snapshotting = nil
		default:
			return
		}
	}
	changed := b.seq - b.lastSnapshotSeq
	if changed == 0 || (changed < b.cfg.SnapshotEvery && b.cfg.Now().Sub(b.lastSnapshotAt) < b.cfg.SnapshotInterval) {
		return
	}
	snap := b.buildSnapshot()
	b.lastSnapshotSeq, b.lastSnapshotAt = snap.Seq, b.cfg.Now()
	done := make(chan struct{})
	b.snapshotting = done
	go func() {
		defer close(done)
		if err := b.writeSnapshot(snap); err != nil {
			b.log.Warn("snapshot failed", "seq", snap.Seq, "err", err)
		}
	}()
}

func (b *Board) writeSnapshot(snap *pb.BoardSnapshot) error {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), b.cfg.IOTimeout)
	defer cancel()
	err := b.cfg.Store.SaveSnapshot(ctx, b.id, snap)
	if err != nil {
		b.metrics.SnapshotFailures.Inc()
		return err
	}
	b.metrics.SnapshotsWritten.Inc()
	b.metrics.SnapshotDuration.Observe(time.Since(start).Seconds())
	return nil
}

// shutdown commits, writes a final snapshot if anything changed, drops any
// remaining clients, and unloads.
func (b *Board) shutdown() error {
	defer b.finish(nil)
	if err := b.flush(); err != nil {
		b.kickAll(KickReload)
		return err
	}
	b.kickAll(KickReload)
	if b.snapshotting != nil {
		<-b.snapshotting
	}
	if b.seq > b.lastSnapshotSeq {
		if err := b.writeSnapshot(b.buildSnapshot()); err != nil {
			return err
		}
	}
	b.log.Info("board unloaded", "seq", b.seq, "objects", b.doc.Len())
	return nil
}

// fail drops every client and unloads without committing or snapshotting.
// Uncommitted batches were never acked; their clients will resend them.
func (b *Board) fail(stage string, err error) {
	b.log.Error("board failed", "stage", stage, "err", err)
	b.metrics.BoardFailures.WithLabelValues(stage).Inc()
	b.kickAll(KickReload)
	b.finish(fmt.Errorf("board %s failed during %s: %w", b.id, stage, err))
}

func (b *Board) kickAll(reason KickReason) {
	for _, c := range b.clients {
		c.Kick(reason)
	}
	clear(b.clients)
	b.joining = nil
}

// finish removes the board from the registry, then releases waiters.
func (b *Board) finish(err error) {
	b.onClose(b)
	b.err = err
	close(b.done)
}
