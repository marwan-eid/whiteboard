package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	pb "whiteboard/internal/pb/whiteboard/v1"
)

// bouncer is a server that answers each client's next connections with Moved
// (pointing back at itself) as often as told, then welcomes it.
type bouncer struct {
	mu     sync.Mutex
	moves  map[uint64]int // Moved answers still owed, by client id
	socks  []*websocket.Conn
	server *httptest.Server
}

func (b *bouncer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	_, data, err := ws.Read(r.Context())
	if err != nil {
		return
	}
	var hello pb.ClientMessage
	if proto.Unmarshal(data, &hello) != nil {
		return
	}
	id := hello.GetHello().GetClientId()
	b.mu.Lock()
	move := b.moves[id] > 0
	if move {
		b.moves[id]--
	} else {
		b.socks = append(b.socks, ws)
	}
	b.mu.Unlock()
	msg := &pb.ServerMessage{Msg: &pb.ServerMessage_Welcome{Welcome: &pb.Welcome{}}}
	if move {
		msg = &pb.ServerMessage{Msg: &pb.ServerMessage_Moved{Moved: &pb.Moved{NodeId: "elsewhere"}}}
	}
	out, _ := proto.Marshal(msg)
	_ = ws.Write(r.Context(), websocket.MessageBinary, out)
	if move {
		_ = ws.Close(websocket.StatusTryAgainLater, "moved")
		return
	}
	for { // until the test drops it or the client leaves
		if _, _, err := ws.Read(r.Context()); err != nil {
			return
		}
	}
}

// dropAll cuts every welcomed connection, after arming n Moved answers per client.
func (b *bouncer) dropAll(clients []*Client, n int) {
	b.mu.Lock()
	for _, c := range clients {
		b.moves[c.ID()] = n
	}
	socks := b.socks
	b.socks = nil
	b.mu.Unlock()
	for _, ws := range socks {
		ws.CloseNow()
	}
}

// A connection that ends the moment it opens (the server answers Moved) must
// leave a redial running. It once did not: the redial started by that
// disconnect saw the dial that opened the connection still finishing and gave
// up, and the dial reported success, so the client stayed disconnected for good.
func TestRedialSurvivesConnectionsThatEndAtOnce(t *testing.T) {
	b := &bouncer{moves: map[uint64]int{}}
	b.server = httptest.NewServer(b)
	defer b.server.Close()
	url := "ws" + strings.TrimPrefix(b.server.URL, "http")

	clients := make([]*Client, 8)
	for i := range clients {
		c := New(Config{URL: url, BoardID: "b", Reconnect: true, NodeURL: func(string) string { return url }})
		defer c.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := c.Connect(ctx)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		clients[i] = c
	}

	rounds := 40
	if testing.Short() {
		rounds = 5
	}
	for round := range rounds {
		before := make([]int, len(clients))
		for i, c := range clients {
			before[i] = c.Stats().Connects
		}
		b.dropAll(clients, 2) // two Moved answers each: no backoff yet (it starts at three)
		deadline := time.Now().Add(10 * time.Second)
		for i, c := range clients {
			for {
				s := c.Stats()
				if s.Connected && s.Connects >= before[i]+3 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("round %d: client %d did not come back: %+v (had %d connects)", round, i, s, before[i])
				}
				time.Sleep(time.Millisecond)
			}
		}
	}
}
