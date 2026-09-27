// Package client is a Go implementation of the board sync client, with the
// same semantics as the browser client (web/src/sync/session.ts): edits apply
// locally at once, wait in a pending queue until acked, and survive
// disconnects. Tests use it to drive real servers; the load generator uses
// it for editing bots.
package client

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	"whiteboard/internal/doc"
	"whiteboard/internal/hlc"
	pb "whiteboard/internal/pb/whiteboard/v1"
	"whiteboard/internal/protocol"
)

type Config struct {
	URL     string
	BoardID string
	// ClientID defaults to a random id.
	ClientID uint64
	// Reconnect makes the client redial after an unexpected disconnect.
	Reconnect bool
	Now       func() time.Time
	// OnFrame, if set, is called (without locks held) after each frame is applied.
	OnFrame func(*pb.Frame)
}

// ErrOffline is returned when an operation needs a connection the client doesn't have.
var ErrOffline = errors.New("client offline")

type Client struct {
	cfg    Config
	prefix string
	clock  *hlc.Clock

	mu            sync.Mutex
	doc           *doc.Doc
	pending       []*pb.OpBatch
	nextClientSeq uint64
	nextObject    uint64
	serverSeq     uint64
	conn          *websocket.Conn
	dialing       bool
	offline       bool // Offline() called: stay disconnected
	welcomed      chan struct{}
	closed        bool
	resyncs       int
	connects      int
	acked         []uint64 // client seqs the server acked as applied
	cursors       map[uint64][2]float64
}

func New(cfg Config) *Client {
	if cfg.ClientID == 0 {
		cfg.ClientID = rand.Uint64N(protocol.MaxClientID) + 1
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Client{
		cfg:           cfg,
		prefix:        protocol.ObjectIDPrefix(cfg.ClientID),
		clock:         hlc.NewClock(cfg.ClientID, func() int64 { return cfg.Now().UnixMilli() }),
		doc:           doc.New(),
		nextClientSeq: 1,
		welcomed:      make(chan struct{}),
		cursors:       map[uint64][2]float64{},
	}
}

func (c *Client) ID() uint64 { return c.cfg.ClientID }

// NewObjectID returns an id no other client can generate.
func (c *Client) NewObjectID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextObject++
	return c.prefix + strconv.FormatUint(c.nextObject, 36)
}

// Connect goes online and waits until the client is connected and welcomed.
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	c.offline = false
	c.mu.Unlock()
	for {
		c.mu.Lock()
		ready := c.conn != nil && c.isWelcomed()
		welcomed := c.welcomed
		c.mu.Unlock()
		if ready {
			return nil
		}
		if err := c.dial(ctx); err != nil && ctx.Err() != nil {
			return err
		}
		select {
		case <-welcomed:
		case <-time.After(20 * time.Millisecond): // the connection may have been replaced meanwhile
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// dial opens a connection unless one is open or being opened, so the client
// never races itself with two sockets.
func (c *Client) dial(ctx context.Context) error {
	c.mu.Lock()
	if c.conn != nil || c.dialing || c.offline || c.closed {
		c.mu.Unlock()
		return nil
	}
	c.dialing = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.dialing = false
		c.mu.Unlock()
	}()

	ws, _, err := websocket.Dial(ctx, c.cfg.URL, nil) //nolint:bodyclose // the websocket library owns the handshake response body
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	ws.SetReadLimit(64 << 20)
	hello := &pb.ClientMessage{Msg: &pb.ClientMessage_Hello{Hello: &pb.Hello{
		ProtocolVersion: protocol.Version,
		BoardId:         c.cfg.BoardID,
		ClientId:        c.cfg.ClientID,
	}}}
	if err := write(ctx, ws, hello); err != nil {
		ws.CloseNow()
		return fmt.Errorf("send hello: %w", err)
	}
	c.mu.Lock()
	if c.offline || c.closed {
		c.mu.Unlock()
		ws.CloseNow()
		return ErrOffline
	}
	c.conn = ws
	c.connects++
	c.mu.Unlock()
	go c.readLoop(ws)
	return nil
}

// Offline drops the connection and stays disconnected until Connect;
// edits made meanwhile queue locally.
func (c *Client) Offline() {
	c.mu.Lock()
	c.offline = true
	ws := c.conn
	c.conn = nil
	c.welcomed = make(chan struct{})
	c.mu.Unlock()
	if ws != nil {
		ws.CloseNow()
	}
}

// Drop simulates a network failure: the connection dies without a close
// handshake. With Reconnect set, the client redials by itself.
func (c *Client) Drop() {
	c.mu.Lock()
	ws := c.conn
	c.mu.Unlock()
	if ws != nil {
		ws.CloseNow()
	}
}

// Close disconnects for good.
func (c *Client) Close() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.Offline()
}

// Edit stamps ops, applies them locally, queues them, and sends them if connected.
func (c *Client) Edit(ops ...*pb.Op) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.clock.Now()
	batch := &pb.OpBatch{ClientSeq: c.nextClientSeq, Stamp: st.Proto(), Ops: ops}
	c.nextClientSeq++
	c.doc.ApplyBatch(ops, st)
	c.pending = append(c.pending, batch)
	if c.conn != nil && c.isWelcomed() {
		c.sendLocked(batch)
	}
}

func (c *Client) isWelcomed() bool {
	select {
	case <-c.welcomed:
		return true
	default:
		return false
	}
}

// sendLocked writes one batch; a failed write is left to the read loop,
// which notices the dead socket and reconnects.
func (c *Client) sendLocked(b *pb.OpBatch) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = write(ctx, c.conn, &pb.ClientMessage{Msg: &pb.ClientMessage_OpBatch{OpBatch: b}})
}

func (c *Client) readLoop(ws *websocket.Conn) {
	for {
		_, data, err := ws.Read(context.Background())
		if err != nil {
			c.onDisconnect(ws)
			return
		}
		var msg pb.ServerMessage
		if err := proto.Unmarshal(data, &msg); err != nil {
			ws.CloseNow()
			continue
		}
		switch m := msg.Msg.(type) {
		case *pb.ServerMessage_Welcome:
			c.onWelcome(ws, m.Welcome)
		case *pb.ServerMessage_Frame:
			if c.onFrame(ws, m.Frame) && c.cfg.OnFrame != nil {
				c.cfg.OnFrame(m.Frame)
			}
		case *pb.ServerMessage_Error:
			ws.CloseNow()
		}
	}
}

func (c *Client) onDisconnect(ws *websocket.Conn) {
	c.mu.Lock()
	if c.conn == ws {
		c.conn = nil
		c.welcomed = make(chan struct{})
	}
	redial := c.cfg.Reconnect && !c.offline && !c.closed && c.conn == nil
	c.mu.Unlock()
	if redial {
		go c.redial()
	}
}

func (c *Client) redial() {
	for attempt := 0; ; attempt++ {
		c.mu.Lock()
		stop := c.offline || c.closed || c.conn != nil || c.dialing
		c.mu.Unlock()
		if stop {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := c.dial(ctx)
		cancel()
		if err == nil {
			return
		}
		time.Sleep(min(time.Duration(10<<min(attempt, 6))*time.Millisecond, time.Second))
	}
}

// onWelcome replaces the local replica with the server's snapshot, then
// reapplies and resends whatever the server hasn't applied yet.
func (c *Client) onWelcome(ws *websocket.Conn, w *pb.Welcome) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != ws {
		return
	}
	d, err := doc.FromSnapshot(w.GetObjects())
	if err != nil {
		ws.CloseNow()
		return
	}
	c.doc = d
	c.serverSeq = w.GetSeq()
	clear(c.cursors) // positions are re-sent as people move
	kept := c.pending[:0]
	for _, b := range c.pending {
		if b.GetClientSeq() > w.GetLastClientSeq() {
			kept = append(kept, b)
		}
	}
	c.pending = kept
	for _, b := range c.pending {
		c.doc.ApplyBatch(b.GetOps(), hlc.FromProto(b.GetStamp()))
	}
	for _, os := range w.GetObjects() {
		for _, fs := range os.GetStamps() {
			c.clock.Observe(hlc.FromProto(fs.GetStamp()))
		}
	}
	close(c.welcomed)
	for _, b := range c.pending {
		c.sendLocked(b)
	}
}

// onFrame applies other clients' batches and settles acked ones. It reports
// false if the frame was ignored (stale socket) or forced a resync.
func (c *Client) onFrame(ws *websocket.Conn, f *pb.Frame) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != ws {
		return false
	}
	maxSeq := c.serverSeq
	for _, b := range f.GetBatches() {
		if b.GetSeq() <= c.serverSeq {
			continue
		}
		st := hlc.FromProto(b.GetStamp())
		c.doc.ApplyBatch(b.GetOps(), st)
		c.clock.Observe(st)
		maxSeq = max(maxSeq, b.GetSeq())
	}
	for _, cu := range f.GetCursors() {
		if cu.GetGone() {
			delete(c.cursors, cu.GetClientId())
		} else {
			c.cursors[cu.GetClientId()] = [2]float64{cu.GetX(), cu.GetY()}
		}
	}
	resync := false
	for _, a := range f.GetAcks() {
		i := c.pendingIndex(a.GetClientSeq())
		if i < 0 {
			continue
		}
		sent := c.pending[i]
		c.pending = append(c.pending[:i], c.pending[i+1:]...)
		switch {
		case a.GetRejected():
			resync = true
		case a.GetSeq() != 0:
			c.acked = append(c.acked, a.GetClientSeq())
			// The server applied our stamp unless it clamped it; if it did, our
			// local replica holds values under a stamp nobody else has.
			if s, as := sent.GetStamp(), a.GetStamp(); s.GetWallMs() != as.GetWallMs() || s.GetCounter() != as.GetCounter() {
				resync = true
			}
			maxSeq = max(maxSeq, a.GetSeq())
		}
	}
	c.serverSeq = maxSeq
	if resync {
		// Reconnecting yields a fresh snapshot; pending edits are reapplied on top.
		c.resyncs++
		ws.CloseNow()
		return false
	}
	return true
}

func (c *Client) pendingIndex(clientSeq uint64) int {
	for i, b := range c.pending {
		if b.GetClientSeq() == clientSeq {
			return i
		}
	}
	return -1
}

// MoveCursor sends this client's pointer position if connected.
func (c *Client) MoveCursor(x, y float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || !c.isWelcomed() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = write(ctx, c.conn, &pb.ClientMessage{Msg: &pb.ClientMessage_Cursor{Cursor: &pb.Cursor{X: x, Y: y}}})
}

// Cursors returns the other clients' cursor positions this client has seen.
func (c *Client) Cursors() map[uint64][2]float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[uint64][2]float64, len(c.cursors))
	for id, p := range c.cursors {
		out[id] = p
	}
	return out
}

// Snapshot returns the local replica in canonical form.
func (c *Client) Snapshot() []*pb.ObjectState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.doc.Snapshot()
}

// Object returns a copy of one local object's properties, or nil.
func (c *Client) Object(id string) *pb.ObjectProps {
	c.mu.Lock()
	defer c.mu.Unlock()
	if o := c.doc.Get(id); o != nil {
		return proto.CloneOf(o.Props)
	}
	return nil
}

// Acked returns the client seqs the server acknowledged as applied (not
// duplicates or rejections), for durability checks.
func (c *Client) Acked() []uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]uint64(nil), c.acked...)
}

// Stats reports sync progress, for tests and the load generator.
type Stats struct {
	ServerSeq uint64
	Pending   int
	Connected bool
	Connects  int
	Resyncs   int
}

func (c *Client) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Stats{
		ServerSeq: c.serverSeq,
		Pending:   len(c.pending),
		Connected: c.conn != nil && c.isWelcomed(),
		Connects:  c.connects,
		Resyncs:   c.resyncs,
	}
}

func write(ctx context.Context, ws *websocket.Conn, msg *pb.ClientMessage) error {
	data, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	return ws.Write(ctx, websocket.MessageBinary, data)
}
