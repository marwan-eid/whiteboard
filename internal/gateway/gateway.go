// Package gateway terminates client WebSocket connections: handshake,
// protocol validation, per-connection send queues, and routing of client
// messages to board actors.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/time/rate"
	"google.golang.org/protobuf/proto"

	"whiteboard/internal/access"
	"whiteboard/internal/board"
	"whiteboard/internal/metrics"
	pb "whiteboard/internal/pb/whiteboard/v1"
	"whiteboard/internal/protocol"
	"whiteboard/internal/ratelimit"
)

type Config struct {
	// HelloTimeout bounds how long a new connection may take to send Hello.
	HelloTimeout time.Duration
	// WriteTimeout bounds each outgoing frame.
	WriteTimeout time.Duration
	// MaxMessageBytes caps a single incoming frame.
	MaxMessageBytes int64
	// SendQueue is how many outgoing messages may wait per connection before
	// the client is considered too slow and disconnected.
	SendQueue int
	// MinCursorInterval drops cursor updates that arrive faster than this.
	MinCursorInterval time.Duration
	// Authorizer decides board access (default: everyone may edit).
	Authorizer access.Authorizer
	// Signer verifies guest tokens (default: tokens are ignored).
	Signer *access.Signer
	// Edited, if set, is told the first time each connection's guest edits
	// (usage counts; it must not block).
	Edited func(guestID string)
	// ConnsPerIP caps open connections per client IP (default: no limit).
	ConnsPerIP *ratelimit.Counter
	// TrustProxy takes the client IP from X-Forwarded-For (see config.TrustProxy).
	TrustProxy bool
	// Per-connection token buckets, refilled per second, with two seconds of
	// burst. A client over them is slowed down (its messages wait), not
	// disconnected. The defaults leave room for a fast pointer drag, which
	// sends a batch per pointer event.
	BatchesPerSec, OpsPerSec, BytesPerSec float64
	Now                                   func() time.Time
}

type Gateway struct {
	cfg      Config
	log      *slog.Logger
	metrics  *metrics.Metrics
	boards   *board.Registry
	shutdown chan struct{}
	once     sync.Once
}

// errProtocol marks connections we closed because the client broke the protocol.
var errProtocol = errors.New("protocol violation")

func New(cfg Config, boards *board.Registry, log *slog.Logger, m *metrics.Metrics) *Gateway {
	if cfg.HelloTimeout == 0 {
		cfg.HelloTimeout = 5 * time.Second
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = 5 * time.Second
	}
	if cfg.MaxMessageBytes == 0 {
		cfg.MaxMessageBytes = 256 << 10
	}
	if cfg.SendQueue == 0 {
		cfg.SendQueue = 256
	}
	if cfg.Authorizer == nil {
		cfg.Authorizer = access.Open{}
	}
	if cfg.MinCursorInterval == 0 {
		cfg.MinCursorInterval = 40 * time.Millisecond
	}
	if cfg.BatchesPerSec == 0 {
		cfg.BatchesPerSec = 240
	}
	if cfg.OpsPerSec == 0 {
		cfg.OpsPerSec = 2000
	}
	if cfg.BytesPerSec == 0 {
		cfg.BytesPerSec = 1 << 20
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Gateway{cfg: cfg, boards: boards, log: log, metrics: m, shutdown: make(chan struct{})}
}

// Shutdown asks every open and future connection to close with
// StatusGoingAway, so clients reconnect. It does not wait for them.
func (g *Gateway) Shutdown() {
	g.once.Do(func() { close(g.shutdown) })
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ip := ratelimit.ClientIP(r, g.cfg.TrustProxy)
	if g.cfg.ConnsPerIP != nil {
		if !g.cfg.ConnsPerIP.Acquire(ip) {
			g.metrics.LimitRejections.WithLabelValues("connections").Inc()
			http.Error(w, "too many connections from this address", http.StatusTooManyRequests)
			return
		}
		defer g.cfg.ConnsPerIP.Release(ip)
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		g.log.Debug("websocket accept failed", "err", err, "remote", r.RemoteAddr)
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(g.cfg.MaxMessageBytes)

	g.metrics.WSConnections.Inc()
	defer g.metrics.WSConnections.Dec()

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-g.shutdown:
			_ = c.Close(websocket.StatusGoingAway, "server shutting down")
		case <-done:
		}
	}()

	err = g.serve(r.Context(), c, ip)
	switch status := websocket.CloseStatus(err); {
	case errors.Is(err, errProtocol):
		g.log.Info("closed connection", "reason", err, "remote", r.RemoteAddr)
	case status == websocket.StatusNormalClosure, status == websocket.StatusGoingAway:
		g.log.Debug("connection closed", "status", status, "remote", r.RemoteAddr)
	default:
		g.log.Debug("connection ended", "err", err, "remote", r.RemoteAddr)
	}
}

func (g *Gateway) serve(ctx context.Context, c *websocket.Conn, clientIP string) error {
	msg, _, err := g.read(ctx, c, g.cfg.HelloTimeout)
	if err != nil {
		return err
	}
	hello := msg.GetHello()
	switch {
	case hello == nil:
		return g.reject(ctx, c, pb.ErrorCode_ERROR_CODE_BAD_REQUEST, "first message must be hello")
	case hello.GetProtocolVersion() != protocol.Version:
		return g.reject(ctx, c, pb.ErrorCode_ERROR_CODE_UNSUPPORTED_VERSION,
			fmt.Sprintf("server speaks protocol version %d", protocol.Version))
	case !protocol.ValidBoardID(hello.GetBoardId()):
		return g.reject(ctx, c, pb.ErrorCode_ERROR_CODE_BAD_REQUEST, "invalid board id")
	case !protocol.ValidClientID(hello.GetClientId()):
		return g.reject(ctx, c, pb.ErrorCode_ERROR_CODE_BAD_REQUEST, "invalid client id")
	case hello.Viewport != nil && !validViewport(hello.GetViewport()):
		return g.reject(ctx, c, pb.ErrorCode_ERROR_CODE_BAD_REQUEST, "invalid viewport")
	}

	var guestID string
	if g.cfg.Signer != nil && hello.GetGuestToken() != "" {
		guestID, _ = g.cfg.Signer.Verify(hello.GetGuestToken()) // an invalid token just means anonymous
	}
	grant, err := g.cfg.Authorizer.Authorize(ctx, access.Request{
		BoardID: hello.GetBoardId(), GuestID: guestID, ShareToken: hello.GetShareToken(), ClientIP: clientIP,
	})
	if errors.Is(err, access.ErrTooManyBoards) {
		g.metrics.LimitRejections.WithLabelValues("board_creates").Inc()
		return g.reject(ctx, c, pb.ErrorCode_ERROR_CODE_FORBIDDEN, "too many new boards from this address; try again later")
	}
	if errors.Is(err, access.ErrForbidden) {
		return g.reject(ctx, c, pb.ErrorCode_ERROR_CODE_FORBIDDEN, "no access to this board")
	}
	if err != nil {
		_ = c.Close(websocket.StatusTryAgainLater, "board unavailable")
		return err
	}

	conn := &clientConn{id: hello.GetClientId(), ws: c, send: make(chan []byte, g.cfg.SendQueue), writeTimeout: g.cfg.WriteTimeout}
	b, err := g.boards.JoinAs(ctx, hello.GetBoardId(), conn, hello.GetViewport(), grant.Role, grant.LinkID)
	if err != nil {
		if moved := (*board.MovedError)(nil); errors.As(err, &moved) {
			// Another node serves this board: send the client there.
			if data, merr := proto.Marshal(&pb.ServerMessage{Msg: &pb.ServerMessage_Moved{Moved: &pb.Moved{NodeId: moved.Node}}}); merr == nil {
				wctx, cancel := context.WithTimeout(ctx, g.cfg.WriteTimeout)
				_ = c.Write(wctx, websocket.MessageBinary, data)
				cancel()
			}
			g.metrics.ClientsMoved.Inc()
			_ = c.Close(websocket.StatusTryAgainLater, "moved")
			return nil
		}
		// Usually the board failed to load (database down); the client retries with backoff.
		_ = c.Close(websocket.StatusTryAgainLater, "board unavailable")
		return err
	}
	defer b.Leave(conn)

	writerDone := make(chan struct{})
	defer func() { <-writerDone }()
	writeCtx, stopWriter := context.WithCancel(ctx)
	defer stopWriter()
	go func() {
		defer close(writerDone)
		g.writeLoop(writeCtx, c, conn.send)
	}()

	// History requests run on their own goroutine so rebuilding old states
	// never blocks this connection; a newer request replaces a waiting one.
	view := hello.GetViewport()
	history := make(chan historyJob, 1)
	defer close(history)
	go g.historyLoop(ctx, b, conn, history)

	lim := g.newLimits()
	var lastCursor time.Time
	edited := false
	for {
		msg, size, err := g.read(ctx, c, 0)
		if err != nil {
			return err
		}
		if err := g.throttle(ctx, lim.bytes, size, "bytes"); err != nil {
			return err
		}
		switch m := msg.Msg.(type) {
		case *pb.ClientMessage_Cursor:
			// Clients send at most ~15 Hz; anything faster is dropped, not an error.
			x, y := m.Cursor.GetX(), m.Cursor.GetY()
			if now := time.Now(); now.Sub(lastCursor) >= g.cfg.MinCursorInterval && validCoord(x) && validCoord(y) {
				lastCursor = now
				b.SetCursor(conn.id, x, y)
			}
		case *pb.ClientMessage_TimePing:
			pong, err := proto.Marshal(&pb.ServerMessage{Msg: &pb.ServerMessage_TimePong{TimePong: &pb.TimePong{
				T0:           m.TimePing.GetT0(),
				ServerTimeMs: g.cfg.Now().UnixMilli(),
			}}})
			if err != nil {
				return err
			}
			if !conn.Send(pong) {
				conn.Kick(board.KickSlow)
			}
		case *pb.ClientMessage_OpBatch:
			if !grant.CanEdit() {
				return g.reject(ctx, c, pb.ErrorCode_ERROR_CODE_FORBIDDEN, "view-only access")
			}
			if err := g.throttle(ctx, lim.batches, 1, "batches"); err != nil {
				return err
			}
			if err := g.throttle(ctx, lim.ops, len(m.OpBatch.GetOps()), "ops"); err != nil {
				return err
			}
			if !edited && g.cfg.Edited != nil {
				edited = true
				g.cfg.Edited(guestID)
			}
			if err := b.Submit(ctx, conn.id, m.OpBatch); err != nil {
				return err
			}
		case *pb.ClientMessage_Viewport:
			if !validViewport(m.Viewport) {
				return g.reject(ctx, c, pb.ErrorCode_ERROR_CODE_BAD_REQUEST, "invalid viewport")
			}
			if err := b.SetViewport(ctx, conn, m.Viewport); err != nil {
				return err
			}
			view = m.Viewport
		case *pb.ClientMessage_History:
			select {
			case <-history:
			default:
			}
			history <- historyJob{seq: m.History.GetSeq(), view: view}
		case *pb.ClientMessage_Restore:
			if !grant.CanEdit() {
				return g.reject(ctx, c, pb.ErrorCode_ERROR_CODE_FORBIDDEN, "view-only access")
			}
			if err := b.Restore(ctx, m.Restore.GetSeq()); err != nil {
				g.log.Warn("restore failed", "seq", m.Restore.GetSeq(), "err", err)
			}
		default:
			return g.reject(ctx, c, pb.ErrorCode_ERROR_CODE_BAD_REQUEST, "unexpected message")
		}
	}
}

// writeLoop drains the connection's send queue onto the socket.
func (g *Gateway) writeLoop(ctx context.Context, c *websocket.Conn, send <-chan []byte) {
	dl := &writeDeadline{parent: ctx, timeout: g.cfg.WriteTimeout}
	defer dl.stop()
	for {
		select {
		case data := <-send:
			err := c.Write(dl.get(), websocket.MessageBinary, data)
			if err != nil {
				c.CloseNow()
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// writeDeadline hands out one deadline context for many writes: allocating
// one per message cost about 7% of a loaded node's CPU. Each write still
// gets at least half of the timeout.
type writeDeadline struct {
	parent  context.Context
	timeout time.Duration
	ctx     context.Context
	cancel  context.CancelFunc
	renewAt time.Time
}

func (d *writeDeadline) get() context.Context {
	if now := time.Now(); d.ctx == nil || now.After(d.renewAt) {
		d.stop()
		d.ctx, d.cancel = context.WithTimeout(d.parent, d.timeout)
		d.renewAt = now.Add(d.timeout / 2)
	}
	return d.ctx
}

func (d *writeDeadline) stop() {
	if d.cancel != nil {
		d.cancel()
	}
}

// read waits for one client message; timeout 0 means no extra deadline.
func (g *Gateway) read(ctx context.Context, c *websocket.Conn, timeout time.Duration) (*pb.ClientMessage, int, error) {
	readCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		readCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	typ, data, err := c.Read(readCtx)
	if err != nil {
		return nil, 0, err
	}
	var msg pb.ClientMessage
	if typ != websocket.MessageBinary || proto.Unmarshal(data, &msg) != nil || msg.Msg == nil {
		g.metrics.WSMessagesIn.WithLabelValues("invalid").Inc()
		return nil, 0, g.reject(ctx, c, pb.ErrorCode_ERROR_CODE_BAD_REQUEST, "malformed message")
	}
	g.metrics.WSMessagesIn.WithLabelValues(messageType(&msg)).Inc()
	return &msg, len(data), nil
}

type limits struct{ batches, ops, bytes *rate.Limiter }

func (g *Gateway) newLimits() limits {
	bucket := func(perSec float64, minBurst int) *rate.Limiter {
		return rate.NewLimiter(rate.Limit(perSec), max(int(2*perSec), minBurst))
	}
	return limits{
		batches: bucket(g.cfg.BatchesPerSec, 1),
		ops:     bucket(g.cfg.OpsPerSec, protocol.MaxOpsPerBatch),
		bytes:   bucket(g.cfg.BytesPerSec, int(g.cfg.MaxMessageBytes)),
	}
}

// throttle waits until the bucket has n tokens.
func (g *Gateway) throttle(ctx context.Context, l *rate.Limiter, n int, name string) error {
	r := l.ReserveN(time.Now(), min(n, l.Burst()))
	d := r.Delay()
	if d == 0 {
		return nil
	}
	g.metrics.Throttled.WithLabelValues(name).Inc()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		r.Cancel()
		return ctx.Err()
	}
}

// reject tells the client why, then closes the connection.
func (g *Gateway) reject(ctx context.Context, c *websocket.Conn, code pb.ErrorCode, reason string) error {
	if data, err := proto.Marshal(&pb.ServerMessage{Msg: &pb.ServerMessage_Error{Error: &pb.ServerError{Code: code, Message: reason}}}); err == nil {
		wctx, cancel := context.WithTimeout(ctx, g.cfg.WriteTimeout)
		_ = c.Write(wctx, websocket.MessageBinary, data)
		cancel()
	}
	_ = c.Close(websocket.StatusPolicyViolation, reason)
	return fmt.Errorf("%w: %s", errProtocol, reason)
}

func messageType(msg *pb.ClientMessage) string {
	switch msg.Msg.(type) {
	case *pb.ClientMessage_Hello:
		return "hello"
	case *pb.ClientMessage_TimePing:
		return "time_ping"
	case *pb.ClientMessage_OpBatch:
		return "op_batch"
	case *pb.ClientMessage_Cursor:
		return "cursor"
	case *pb.ClientMessage_Viewport:
		return "viewport"
	case *pb.ClientMessage_History:
		return "history"
	case *pb.ClientMessage_Restore:
		return "restore"
	default:
		return "unknown"
	}
}

// validViewport allows rectangles up to 4x the coordinate range per side, so
// a client can ask for everything.
func validViewport(v *pb.Viewport) bool {
	size := func(s float64) bool { return !math.IsNaN(s) && s >= 0 && s <= 4*protocol.MaxCoord }
	pos := func(p float64) bool { return !math.IsNaN(p) && math.Abs(p) <= 2*protocol.MaxCoord }
	return pos(v.GetX()) && pos(v.GetY()) && size(v.GetW()) && size(v.GetH())
}

func validCoord(v float64) bool {
	return !math.IsNaN(v) && math.Abs(v) <= protocol.MaxCoord
}

// clientConn implements board.Conn for one WebSocket.
type clientConn struct {
	id           uint64
	ws           *websocket.Conn
	send         chan []byte
	writeTimeout time.Duration
	once         sync.Once
}

func (c *clientConn) ClientID() uint64 { return c.id }

func (c *clientConn) Send(msg []byte) bool {
	select {
	case c.send <- msg:
		return true
	default:
		return false
	}
}

// Kick closes the socket. Close runs asynchronously because it waits for the
// peer's close frame, and Kick is called from the board goroutine.
func (c *clientConn) Kick(reason board.KickReason) {
	c.once.Do(func() {
		go func() {
			switch reason {
			case board.KickReplaced:
				// Tell the client not to reconnect: another connection now owns its id.
				const msg = "replaced by a newer connection with the same client id"
				if data, err := proto.Marshal(&pb.ServerMessage{Msg: &pb.ServerMessage_Error{Error: &pb.ServerError{
					Code: pb.ErrorCode_ERROR_CODE_CLIENT_ID_IN_USE, Message: msg,
				}}}); err == nil {
					ctx, cancel := context.WithTimeout(context.Background(), c.writeTimeout)
					_ = c.ws.Write(ctx, websocket.MessageBinary, data)
					cancel()
				}
				_ = c.ws.Close(websocket.StatusPolicyViolation, "client id in use")
			case board.KickRevoked:
				c.sendError(pb.ErrorCode_ERROR_CODE_FORBIDDEN, "access revoked")
				_ = c.ws.Close(websocket.StatusPolicyViolation, "access revoked")
			case board.KickReload:
				_ = c.ws.Close(websocket.StatusTryAgainLater, "board reloading")
			default:
				// The client reconnects and gets a fresh snapshot.
				_ = c.ws.Close(websocket.StatusTryAgainLater, "slow consumer")
			}
		}()
	})
}

type historyJob struct {
	seq  uint64
	view *pb.Viewport
}

func (g *Gateway) historyLoop(ctx context.Context, b *board.Board, conn *clientConn, jobs <-chan historyJob) {
	for job := range jobs {
		h, err := b.History(ctx, job.seq, job.view)
		if err != nil {
			g.log.Warn("history failed", "seq", job.seq, "err", err)
			continue
		}
		data, err := proto.Marshal(&pb.ServerMessage{Msg: &pb.ServerMessage_History{History: h}})
		if err != nil {
			continue
		}
		if !conn.Send(data) {
			conn.Kick(board.KickSlow)
		}
	}
}

// sendError writes a ServerError directly (before closing the socket).
func (c *clientConn) sendError(code pb.ErrorCode, msg string) {
	data, err := proto.Marshal(&pb.ServerMessage{Msg: &pb.ServerMessage_Error{Error: &pb.ServerError{Code: code, Message: msg}}})
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.writeTimeout)
	defer cancel()
	_ = c.ws.Write(ctx, websocket.MessageBinary, data)
}
