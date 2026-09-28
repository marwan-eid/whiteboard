package board

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"whiteboard/internal/metrics"
	pb "whiteboard/internal/pb/whiteboard/v1"
)

// Registry holds the boards live on this node, loading them on first join.
type Registry struct {
	cfg     Config
	log     *slog.Logger
	metrics *metrics.Metrics

	mu     sync.Mutex
	boards map[string]*Board
}

func NewRegistry(cfg Config, log *slog.Logger, m *metrics.Metrics) *Registry {
	cfg.setDefaults()
	if cfg.Store == nil {
		panic("board: Config.Store is required")
	}
	return &Registry{cfg: cfg, log: log, metrics: m, boards: map[string]*Board{}}
}

// Join attaches c to the board as an editor; see JoinAs.
func (r *Registry) Join(ctx context.Context, boardID string, c Conn, viewport *pb.Viewport) (*Board, error) {
	return r.JoinAs(ctx, boardID, c, viewport, pb.Role_ROLE_EDITOR, "")
}

// JoinAs attaches c to the board, loading the board if needed. The board
// sends the client a Welcome on its next tick with its role and the objects
// in viewport (the whole board if nil). linkID is the share link that
// granted access, if any.
func (r *Registry) JoinAs(ctx context.Context, boardID string, c Conn, viewport *pb.Viewport, role pb.Role, linkID string) (*Board, error) {
	for {
		b := r.get(boardID)
		err := b.join(ctx, c, viewport, role, linkID)
		if errors.Is(err, errClosed) {
			continue // raced with the board unloading; load it again
		}
		if err != nil {
			return nil, err
		}
		return b, nil
	}
}

// KickLink disconnects everyone on the board who joined through linkID.
func (r *Registry) KickLink(boardID, linkID string) {
	if b := r.Lookup(boardID); b != nil {
		_ = b.send(context.Background(), kickLinkMsg{linkID: linkID})
	}
}

// Lookup returns the live board with this id, if any.
func (r *Registry) Lookup(boardID string) *Board {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.boards[boardID]
}

// Close commits and snapshots every live board, then unloads them.
func (r *Registry) Close(ctx context.Context) error {
	var errs []error
	for _, b := range r.all() {
		if err := b.close(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// CrashAll drops every board without committing or snapshotting, as if the
// process died (for failure tests). Boards reload from the store on next join.
func (r *Registry) CrashAll() {
	for _, b := range r.all() {
		select {
		case b.inbox <- crashMsg{}:
			<-b.done
		case <-b.done:
		}
	}
}

func (r *Registry) all() []*Board {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Board, 0, len(r.boards))
	for _, b := range r.boards {
		out = append(out, b)
	}
	return out
}

func (r *Registry) get(boardID string) *Board {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.boards[boardID]
	if b == nil {
		b = newBoard(boardID, r.cfg, r.log, r.metrics, r.remove)
		r.boards[boardID] = b
	}
	return b
}

// remove runs on the board's goroutine before it closes. Removing the board
// under the lock guarantees no new Join can reach it afterwards.
func (r *Registry) remove(b *Board) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.boards[b.id] == b {
		delete(r.boards, b.id)
	}
}
