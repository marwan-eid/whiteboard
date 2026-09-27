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
	"google.golang.org/protobuf/proto"

	"whiteboard/internal/board"
	"whiteboard/internal/metrics"
	pb "whiteboard/internal/pb/whiteboard/v1"
	"whiteboard/internal/protocol"
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
	Now               func() time.Time
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
	if cfg.MinCursorInterval == 0 {
		cfg.MinCursorInterval = 40 * time.Millisecond
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

	err = g.serve(r.Context(), c)
	switch status := websocket.CloseStatus(err); {
	case errors.Is(err, errProtocol):
		g.log.Info("closed connection", "reason", err, "remote", r.RemoteAddr)
	case status == websocket.StatusNormalClosure, status == websocket.StatusGoingAway:
		g.log.Debug("connection closed", "status", status, "remote", r.RemoteAddr)
	default:
		g.log.Debug("connection ended", "err", err, "remote", r.RemoteAddr)
	}
}

func (g *Gateway) serve(ctx context.Context, c *websocket.Conn) error {
	msg, err := g.read(ctx, c, g.cfg.HelloTimeout)
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
	}

	conn := &clientConn{id: hello.GetClientId(), ws: c, send: make(chan []byte, g.cfg.SendQueue), writeTimeout: g.cfg.WriteTimeout}
	b, err := g.boards.Join(ctx, hello.GetBoardId(), conn)
	if err != nil {
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

	var lastCursor time.Time
	for {
		msg, err := g.read(ctx, c, 0)
		if err != nil {
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
			if err := b.Submit(ctx, conn.id, m.OpBatch); err != nil {
				return err
			}
		default:
			return g.reject(ctx, c, pb.ErrorCode_ERROR_CODE_BAD_REQUEST, "unexpected message")
		}
	}
}

// writeLoop drains the connection's send queue onto the socket.
func (g *Gateway) writeLoop(ctx context.Context, c *websocket.Conn, send <-chan []byte) {
	for {
		select {
		case data := <-send:
			wctx, cancel := context.WithTimeout(ctx, g.cfg.WriteTimeout)
			err := c.Write(wctx, websocket.MessageBinary, data)
			cancel()
			if err != nil {
				c.CloseNow()
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// read waits for one client message; timeout 0 means no extra deadline.
func (g *Gateway) read(ctx context.Context, c *websocket.Conn, timeout time.Duration) (*pb.ClientMessage, error) {
	readCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		readCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	typ, data, err := c.Read(readCtx)
	if err != nil {
		return nil, err
	}
	var msg pb.ClientMessage
	if typ != websocket.MessageBinary || proto.Unmarshal(data, &msg) != nil || msg.Msg == nil {
		g.metrics.WSMessagesIn.WithLabelValues("invalid").Inc()
		return nil, g.reject(ctx, c, pb.ErrorCode_ERROR_CODE_BAD_REQUEST, "malformed message")
	}
	g.metrics.WSMessagesIn.WithLabelValues(messageType(&msg)).Inc()
	return &msg, nil
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
	default:
		return "unknown"
	}
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
			case board.KickReload:
				_ = c.ws.Close(websocket.StatusTryAgainLater, "board reloading")
			default:
				// The client reconnects and gets a fresh snapshot.
				_ = c.ws.Close(websocket.StatusTryAgainLater, "slow consumer")
			}
		}()
	})
}
