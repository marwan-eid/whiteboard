// Package board runs one actor goroutine per live board. The actor is the
// board's single writer: it validates client batches, assigns each a seq and
// merges it into the document. Once per tick it durably appends that tick's
// batches to the log, then sends every client one frame with the parts of
// other clients' batches that touch its viewport, acks for its own, and
// nearby cursors: nothing is acked or shown to others before it is committed.
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
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	"whiteboard/internal/doc"
	"whiteboard/internal/hlc"
	"whiteboard/internal/metrics"
	pb "whiteboard/internal/pb/whiteboard/v1"
	"whiteboard/internal/protocol"
	"whiteboard/internal/spatial"
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
	// KickRevoked: the share link the client joined with was revoked; it must
	// not reconnect with it.
	KickRevoked
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
	// MaxObjects caps the objects a board holds, deleted ones included (they
	// are kept as tombstones). Batches that would create more are rejected.
	MaxObjects int
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
	if c.MaxObjects == 0 {
		c.MaxObjects = 200_000
	}
	if c.Now == nil {
		c.Now = time.Now
	}
}

// rectOK is an object's box, or ok=false if it is not visible.
type rectOK struct {
	r  spatial.Rect
	ok bool
}

// pending is an applied batch waiting for the tick's commit.
type pending struct {
	entry         LogEntry
	before, after []rectOK
	received      time.Time
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
	grid            *spatial.Grid // boxes of visible objects
	seq             uint64
	clock           *hlc.Clock
	lastClientSeq   map[uint64]uint64
	clients         map[uint64]*clientState
	joining         []*clientState
	uncommitted     []pending
	outAcks         map[uint64][]*pb.Ack
	onlineChanged   bool
	idleSince       time.Time
	lastSnapshotSeq uint64
	lastSnapshotAt  time.Time
	snapshotting    chan struct{} // closed when the in-flight snapshot write finishes
	cursorGrid      *spatial.Grid // cursor positions, keyed by base-36 client id
	cursorIDs       map[string]uint64
	goneCursors     []uint64
	presenceMoved   map[uint64]bool // cursors that moved since the last presence tick
	tick            uint64
	workers         []*frameWorker
	targets         []target

	// History reads committed state on callers' goroutines.
	hist         *historian
	committedSeq atomic.Uint64

	// Written by connection goroutines, drained by the actor each tick.
	cursorMu    sync.Mutex
	cursorMoved map[uint64][2]float64
}

type joinMsg struct {
	conn     Conn
	viewport *pb.Viewport
	role     pb.Role
	linkID   string
	reply    chan error
}

type kickLinkMsg struct{ linkID string }

type leaveMsg struct{ conn Conn }

type batchMsg struct {
	clientID uint64
	batch    *pb.OpBatch
	received time.Time
}

type viewportMsg struct {
	conn     Conn
	viewport *pb.Viewport
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
		grid:           spatial.NewGrid(512, 256),
		clock:          hlc.NewClock(0, func() int64 { return cfg.Now().UnixMilli() }),
		lastClientSeq:  map[uint64]uint64{},
		clients:        map[uint64]*clientState{},
		outAcks:        map[uint64][]*pb.Ack{},
		idleSince:      cfg.Now(),
		lastSnapshotAt: cfg.Now(),
		cursorGrid:     spatial.NewGrid(512, 1),
		cursorIDs:      map[string]uint64{},
		cursorMoved:    map[uint64][2]float64{},
		presenceMoved:  map[uint64]bool{},
		hist:           newHistorian(cfg.Store, id),
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

func (b *Board) join(ctx context.Context, c Conn, viewport *pb.Viewport, role pb.Role, linkID string) error {
	reply := make(chan error, 1)
	if err := b.send(ctx, joinMsg{conn: c, viewport: viewport, role: role, linkID: linkID, reply: reply}); err != nil {
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
	return b.send(ctx, batchMsg{clientID: clientID, batch: batch, received: time.Now()})
}

// SetViewport changes the region a connection receives. It is ordered with
// the connection's batches.
func (b *Board) SetViewport(ctx context.Context, c Conn, v *pb.Viewport) error {
	return b.send(ctx, viewportMsg{conn: c, viewport: v})
}

// SetCursor records a client's pointer position for the next tick. Cursors
// bypass the actor's inbox so presence traffic never delays edits; only the
// latest position per client is kept.
func (b *Board) SetCursor(clientID uint64, x, y float64) {
	b.cursorMu.Lock()
	b.cursorMoved[clientID] = [2]float64{x, y}
	b.cursorMu.Unlock()
}

// Snapshot returns the whole current state, including batches not yet committed.
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
	b.doc.Range(func(o *doc.Object) bool {
		if r, ok := bounds(o); ok {
			b.grid.Set(o.ID, r)
		}
		return true
	})
	b.committedSeq.Store(b.seq)
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
			old.conn.Kick(KickReplaced)
			b.remove(old.conn)
		}
		view, lod := viewRect(m.viewport)
		c := &clientState{conn: m.conn, view: view, lod: lod, moves: map[string]bool{}, role: m.role, linkID: m.linkID}
		b.clients[id] = c
		b.joining = append(b.joining, c)
		b.onlineChanged = true
		m.reply <- nil
	case leaveMsg:
		b.remove(m.conn)
	case kickLinkMsg:
		for _, c := range b.clients {
			if c.linkID == m.linkID {
				b.metrics.ClientsKicked.WithLabelValues("revoked").Inc()
				c.conn.Kick(KickRevoked)
				b.remove(c.conn)
			}
		}
	case batchMsg:
		b.apply(m.clientID, m.batch, m.received)
	case viewportMsg:
		if c := b.clients[m.conn.ClientID()]; c != nil && c.conn == m.conn {
			b.setViewport(m.conn.ClientID(), c, m.viewport)
		}
	case snapshotMsg:
		m.reply <- Snapshot{Seq: b.seq, Objects: b.doc.Snapshot()}
	case restoreMsg:
		b.applyRestore(m.past)
		m.reply <- nil
	}
}

func (b *Board) remove(conn Conn) {
	id := conn.ClientID()
	c := b.clients[id]
	if c == nil || c.conn != conn {
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
	b.goneCursors = append(b.goneCursors, id)
	b.onlineChanged = true
	if len(b.clients) == 0 {
		b.idleSince = b.cfg.Now()
	}
}

func (b *Board) apply(clientID uint64, batch *pb.OpBatch, received time.Time) {
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
	if n := b.newObjects(batch.GetOps()); n > 0 && b.doc.Len()+n > b.cfg.MaxObjects {
		b.reject(clientID, cs, fmt.Sprintf("board is full (%d objects)", b.cfg.MaxObjects))
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

	b.applyOps(clientID, cs, batch.GetOps(), st, received)
	b.metrics.BatchesApplied.Inc()
	b.ack(clientID, &pb.Ack{ClientSeq: cs, Seq: b.seq, Stamp: st.Proto()})
}

// newObjects counts the distinct objects a batch would create.
func (b *Board) newObjects(ops []*pb.Op) int {
	var seen map[string]bool
	for _, op := range ops {
		if id := op.GetId(); b.doc.Get(id) == nil && !seen[id] {
			if seen == nil {
				seen = make(map[string]bool)
			}
			seen[id] = true
		}
	}
	return len(seen)
}

// applyOps merges a batch, assigns it the next seq and queues it for commit.
// It records each object's box before and after its op; flush uses them to
// decide who gets the op, the whole object, or nothing.
func (b *Board) applyOps(clientID, clientSeq uint64, ops []*pb.Op, st hlc.Stamp, received time.Time) {
	p := pending{before: make([]rectOK, len(ops)), after: make([]rectOK, len(ops)), received: received}
	for i, op := range ops {
		id := op.GetId()
		r, ok := bounds(b.doc.Get(id))
		p.before[i] = rectOK{r, ok}
		b.doc.Apply(op, st)
		r, ok = bounds(b.doc.Get(id))
		p.after[i] = rectOK{r, ok}
		if ok {
			b.grid.Set(id, r)
		} else {
			b.grid.Remove(id)
		}
	}
	b.seq++
	p.entry = LogEntry{Seq: b.seq, ClientID: clientID, ClientSeq: clientSeq, Stamp: st.Proto(), Ops: ops}
	b.uncommitted = append(b.uncommitted, p)
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
	var batches []encodedBatch
	if len(b.uncommitted) > 0 {
		entries := make([]LogEntry, len(b.uncommitted))
		for i, p := range b.uncommitted {
			entries[i] = p.entry
		}
		ctx, cancel := context.WithTimeout(context.Background(), b.cfg.IOTimeout)
		err := b.cfg.Store.Append(ctx, b.id, entries)
		cancel()
		b.metrics.CommitDuration.Observe(time.Since(start).Seconds())
		if err != nil {
			return err
		}
		b.committedSeq.Store(b.seq)
		batches = make([]encodedBatch, len(b.uncommitted))
		for i, p := range b.uncommitted {
			batches[i] = encodeBatch(p.entry, p.before, p.after)
		}
	}

	for id := range b.updateCursors() {
		b.presenceMoved[id] = true
	}
	b.tick++
	t := &tickFrames{batches: batches, presence: b.tick%presenceEvery == 0, moved: b.presenceMoved, online: -1}
	if b.onlineChanged {
		t.online = len(b.clients)
	}
	joining := map[*clientState]bool{}
	for _, c := range b.joining {
		joining[c] = true
	}
	b.targets = b.targets[:0]
	for id, c := range b.clients {
		if !joining[c] {
			b.targets = append(b.targets, target{id, c})
		}
	}
	b.sendFrames(t, b.targets)
	if t.presence {
		clear(b.presenceMoved)
	}
	clear(b.outAcks)
	b.onlineChanged = false

	for _, c := range b.joining {
		c.refs, c.evaluatedUpTo, c.moves = c.refs[:0], 0, map[string]bool{} // the Welcome covers this tick
		b.deliver(c.conn, &pb.ServerMessage{Msg: &pb.ServerMessage_Welcome{Welcome: &pb.Welcome{
			ProtocolVersion: protocol.Version,
			NodeId:          b.cfg.NodeID,
			ServerTimeMs:    b.cfg.Now().UnixMilli(),
			Seq:             b.seq,
			Objects:         b.objectsIn(c.view, c.lod),
			LastClientSeq:   b.lastClientSeq[c.conn.ClientID()],
			Online:          uint32(len(b.clients)),
			Role:            c.role,
		}}})
		c.presenceStale = true // cursors reach a joiner on its next frame
	}
	b.joining = b.joining[:0]

	// Server-side sync latency: batch received until its frames are queued.
	done := time.Now()
	for _, p := range b.uncommitted {
		b.metrics.SyncServerLatency.Observe(done.Sub(p.received).Seconds())
	}
	b.uncommitted = b.uncommitted[:0]
	b.metrics.TickDuration.Observe(time.Since(start).Seconds())
	return nil
}

// enterEntry returns an object's full (or LOD) state as a Frame.objects
// entry, encoding it at most once per tick.
func (b *Board) enterEntry(cache map[string][]byte, id string, lod bool) []byte {
	key := id
	if lod {
		key = "lod:" + id
	}
	if e, ok := cache[key]; ok {
		return e
	}
	e := appendMessage(nil, fieldFrameObjects, b.objectState(id, lod))
	cache[key] = e
	return e
}

// updateCursors moves this tick's cursors in the cursor grid, drops those of
// departed clients, and returns the clients whose cursor changed. Moves from
// clients no longer on the board are ignored, so a cursor message racing a
// departure cannot leave a ghost behind.
func (b *Board) updateCursors() map[uint64]bool {
	// Take the map under the lock; connection goroutines keep writing to its
	// replacement. (Never touch the shared map outside the lock.)
	b.cursorMu.Lock()
	positions := b.cursorMoved
	if len(positions) > 0 {
		b.cursorMoved = map[uint64][2]float64{}
	} else {
		positions = nil
	}
	b.cursorMu.Unlock()

	moved := map[uint64]bool{}
	for _, id := range b.goneCursors {
		delete(positions, id)
		if _, back := b.clients[id]; back {
			continue
		}
		key := strconv.FormatUint(id, 36)
		if _, had := b.cursorIDs[key]; had {
			b.cursorGrid.Remove(key)
			delete(b.cursorIDs, key)
			moved[id] = true
		}
	}
	b.goneCursors = b.goneCursors[:0]
	for id, p := range positions {
		if _, ok := b.clients[id]; !ok {
			continue
		}
		key := strconv.FormatUint(id, 36)
		b.cursorIDs[key] = id
		b.cursorGrid.Set(key, spatial.Rect{X: p[0], Y: p[1]})
		moved[id] = true
	}
	return moved
}

func (b *Board) deliver(c Conn, msg *pb.ServerMessage) {
	data, err := proto.Marshal(msg)
	if err != nil {
		b.log.Error("marshal server message", "err", err)
		return
	}
	b.deliverBytes(c, data)
}

func (b *Board) deliverBytes(c Conn, data []byte) {
	if !c.Send(data) {
		b.metrics.ClientsKicked.WithLabelValues("slow_consumer").Inc()
		c.Kick(KickSlow)
		b.remove(c)
		return
	}
	b.metrics.FanoutBytes.Add(float64(len(data)))
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
			return
		}
		b.compact(snap.Seq)
	}()
}

// compact packs log entries up to a snapshot into compressed segments; loading
// never needs them again, only history does.
func (b *Board) compact(upTo uint64) {
	ctx, cancel := context.WithTimeout(context.Background(), b.cfg.IOTimeout)
	defer cancel()
	if n, err := b.cfg.Store.Compact(ctx, b.id, upTo); err != nil {
		b.log.Warn("compaction failed", "up_to", upTo, "err", err)
	} else if n > 0 {
		b.log.Debug("compacted log", "entries", n, "up_to", upTo)
	}
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
		c.conn.Kick(reason)
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

func sortedMoves(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
