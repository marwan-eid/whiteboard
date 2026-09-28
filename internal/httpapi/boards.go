package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"whiteboard/internal/access"
	"whiteboard/internal/metrics"
	pb "whiteboard/internal/pb/whiteboard/v1"
	"whiteboard/internal/ratelimit"
)

// Boards is the board and share-link management the API exposes
// (implemented by access.Postgres).
type Boards interface {
	CreateBoard(ctx context.Context, guestID, title string) (access.Board, error)
	ListBoards(ctx context.Context, guestID string) ([]access.Board, error)
	CreateLink(ctx context.Context, boardID, guestID string, role pb.Role) (access.Link, error)
	ListLinks(ctx context.Context, boardID, guestID string) ([]access.Link, error)
	RevokeLink(ctx context.Context, boardID, guestID, linkID string) error
}

type boardAPI struct {
	signer     *access.Signer
	boards     Boards
	kickLink   func(boardID, linkID string)
	creates    *ratelimit.Keyed
	trustProxy bool
	metrics    *metrics.Metrics
}

func (a *boardAPI) register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/guest", a.guest)
	mux.HandleFunc("POST /api/boards", a.authed(a.createBoard))
	mux.HandleFunc("GET /api/boards", a.authed(a.listBoards))
	mux.HandleFunc("POST /api/boards/{board}/links", a.authed(a.createLink))
	mux.HandleFunc("GET /api/boards/{board}/links", a.authed(a.listLinks))
	mux.HandleFunc("DELETE /api/boards/{board}/links/{link}", a.authed(a.revokeLink))
}

// guest issues a new guest identity; clients keep it in local storage.
func (a *boardAPI) guest(w http.ResponseWriter, _ *http.Request) {
	token, id := a.signer.Issue()
	writeJSON(w, http.StatusCreated, map[string]string{"token": token, "guestId": id})
}

func (a *boardAPI) authed(h func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		guestID, valid := a.signer.Verify(token)
		if !ok || !valid {
			http.Error(w, "missing or invalid guest token", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
		h(w, r, guestID)
	}
}

func (a *boardAPI) createBoard(w http.ResponseWriter, r *http.Request, guestID string) {
	if a.creates != nil && !a.creates.Allow(ratelimit.ClientIP(r, a.trustProxy)) {
		a.metrics.LimitRejections.WithLabelValues("board_creates").Inc()
		http.Error(w, "too many new boards from this address; try again later", http.StatusTooManyRequests)
		return
	}
	var body struct {
		Title string `json:"title"`
	}
	if r.ContentLength != 0 && json.NewDecoder(r.Body).Decode(&body) != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if len(body.Title) > 200 {
		http.Error(w, "title too long", http.StatusBadRequest)
		return
	}
	b, err := a.boards.CreateBoard(r.Context(), guestID, body.Title)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, b)
}

func (a *boardAPI) listBoards(w http.ResponseWriter, r *http.Request, guestID string) {
	bs, err := a.boards.ListBoards(r.Context(), guestID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, bs)
}

func (a *boardAPI) createLink(w http.ResponseWriter, r *http.Request, guestID string) {
	var body struct {
		Role string `json:"role"`
	}
	role, ok := pb.Role(0), false
	if json.NewDecoder(r.Body).Decode(&body) == nil {
		role, ok = access.ParseRole(body.Role)
	}
	if !ok {
		http.Error(w, `role must be "viewer" or "editor"`, http.StatusBadRequest)
		return
	}
	l, err := a.boards.CreateLink(r.Context(), r.PathValue("board"), guestID, role)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, l)
}

func (a *boardAPI) listLinks(w http.ResponseWriter, r *http.Request, guestID string) {
	ls, err := a.boards.ListLinks(r.Context(), r.PathValue("board"), guestID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ls)
}

func (a *boardAPI) revokeLink(w http.ResponseWriter, r *http.Request, guestID string) {
	boardID, linkID := r.PathValue("board"), r.PathValue("link")
	if err := a.boards.RevokeLink(r.Context(), boardID, guestID, linkID); err != nil {
		writeError(w, err)
		return
	}
	// People connected through the link lose access now, not at their next reconnect.
	if a.kickLink != nil {
		a.kickLink(boardID, linkID)
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, access.ErrNotOwner):
		http.Error(w, "only the board owner can do this", http.StatusForbidden)
	case errors.Is(err, access.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
