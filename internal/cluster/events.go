package cluster

import (
	"context"
	"encoding/json"
	"time"
)

// Board events reach every node through Postgres NOTIFY, so an action taken
// on whichever node served the HTTP request (say, revoking a share link)
// applies on the node serving the board. Delivery is best effort: a node
// whose listener is reconnecting misses events. For revocation that only
// delays the kick until the user reconnects, since joins are authorized
// against the database.
const eventChannel = "whiteboard_events"

type event struct {
	Kind  string `json:"kind"`
	Board string `json:"board"`
	Link  string `json:"link,omitempty"`
}

// KickLink asks every node to disconnect the users of a share link.
func (n *Node) KickLink(ctx context.Context, boardID, linkID string) error {
	payload, err := json.Marshal(event{Kind: "kick_link", Board: boardID, Link: linkID})
	if err != nil {
		return err
	}
	_, err = n.pool.Exec(ctx, "SELECT pg_notify($1, $2)", eventChannel, string(payload))
	return err
}

// Listen delivers board events until ctx ends, reconnecting after errors.
func (n *Node) Listen(ctx context.Context, onKickLink func(boardID, linkID string)) {
	for ctx.Err() == nil {
		if err := n.listen(ctx, onKickLink); err != nil && ctx.Err() == nil {
			n.log.Warn("board event listener failed; reconnecting", "err", err)
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		}
	}
}

func (n *Node) listen(ctx context.Context, onKickLink func(boardID, linkID string)) error {
	conn, err := n.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	// LISTEN is per connection; this one never goes back to the pool.
	pc := conn.Hijack()
	defer func() { _ = pc.Close(context.Background()) }()
	if _, err := pc.Exec(ctx, "LISTEN "+eventChannel); err != nil {
		return err
	}
	for {
		note, err := pc.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		var e event
		if json.Unmarshal([]byte(note.Payload), &e) != nil {
			continue
		}
		if e.Kind == "kick_link" {
			onKickLink(e.Board, e.Link)
		}
	}
}
