package gateway

import (
	"context"
	"sync"
	"testing"

	"github.com/coder/websocket"

	"whiteboard/internal/access"
	pb "whiteboard/internal/pb/whiteboard/v1"
	"whiteboard/internal/protocol"
)

// fakeAuth grants by share token and records the guest ids it saw.
type fakeAuth struct {
	mu     sync.Mutex
	grants map[string]access.Grant // share token -> grant; missing means forbidden
	guests []string
}

func (f *fakeAuth) Authorize(_ context.Context, _, guestID, token string) (access.Grant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.guests = append(f.guests, guestID)
	g, ok := f.grants[token]
	if !ok {
		return access.Grant{}, access.ErrForbidden
	}
	return g, nil
}

func helloWith(board, guestToken, shareToken string) *pb.ClientMessage {
	msg := hello(protocol.Version, board)
	msg.GetHello().GuestToken = guestToken
	msg.GetHello().ShareToken = shareToken
	return msg
}

func TestAccessEnforced(t *testing.T) {
	signer := access.NewSigner([]byte("test"))
	auth := &fakeAuth{grants: map[string]access.Grant{
		"view": {Role: pb.Role_ROLE_VIEWER, LinkID: "L1"},
		"edit": {Role: pb.Role_ROLE_EDITOR, LinkID: "L2"},
	}}
	h := newHarness(t, Config{Authorizer: auth, Signer: signer})

	t.Run("no grant is rejected", func(t *testing.T) {
		c := h.dial(t)
		send(t, c, helloWith("secret", "", ""))
		expectRejected(t, c, pb.ErrorCode_ERROR_CODE_FORBIDDEN)
	})

	t.Run("guest token is verified", func(t *testing.T) {
		token, id := signer.Issue()
		for _, tok := range []string{token, token + "x"} {
			c := h.dial(t)
			join(t, c, helloWith("demo", tok, "edit"))
		}
		auth.mu.Lock()
		got := auth.guests[len(auth.guests)-2:]
		auth.mu.Unlock()
		if got[0] != id || got[1] != "" {
			t.Fatalf("guest ids = %q, want [%q, \"\"] (a forged token is anonymous)", got, id)
		}
	})

	t.Run("viewer can read but not edit", func(t *testing.T) {
		c := h.dial(t)
		if w := join(t, c, helloWith("demo", "", "view")); w.GetRole() != pb.Role_ROLE_VIEWER {
			t.Fatalf("role = %v", w.GetRole())
		}
		send(t, c, &pb.ClientMessage{Msg: &pb.ClientMessage_Restore{Restore: &pb.RestoreRequest{Seq: 0}}})
		expectRejected(t, c, pb.ErrorCode_ERROR_CODE_FORBIDDEN)

		c = h.dial(t)
		join(t, c, helloWith("demo", "", "view"))
		send(t, c, &pb.ClientMessage{Msg: &pb.ClientMessage_OpBatch{OpBatch: &pb.OpBatch{ClientSeq: 1}}})
		expectRejected(t, c, pb.ErrorCode_ERROR_CODE_FORBIDDEN)
	})

	t.Run("revoking a link disconnects its users only", func(t *testing.T) {
		viaView, viaEdit := h.dial(t), h.dial(t)
		join(t, viaView, helloWith("revoke", "", "view"))
		if w := join(t, viaEdit, helloWith("revoke", "", "edit")); w.GetRole() != pb.Role_ROLE_EDITOR {
			t.Fatalf("role = %v", w.GetRole())
		}
		h.boards.KickLink("revoke", "L1")
		for {
			msg := recv(t, viaView)
			if msg.GetFrame() == nil {
				if msg.GetError().GetCode() != pb.ErrorCode_ERROR_CODE_FORBIDDEN {
					t.Fatalf("got %v, want a FORBIDDEN error", msg)
				}
				break
			}
		}
		if _, err := tryRecv(viaView); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
			t.Fatalf("revoked connection not closed: %v", err)
		}
		// The other connection still works.
		send(t, viaEdit, &pb.ClientMessage{Msg: &pb.ClientMessage_TimePing{TimePing: &pb.TimePing{T0: 1}}})
		for {
			msg := recv(t, viaEdit)
			if msg.GetTimePong() != nil {
				break
			}
		}
	})
}
