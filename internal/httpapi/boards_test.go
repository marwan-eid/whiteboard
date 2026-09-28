package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"whiteboard/internal/access"
	"whiteboard/internal/metrics"
	pb "whiteboard/internal/pb/whiteboard/v1"
	"whiteboard/internal/ratelimit"
)

// fakeBoards: board "b1" belongs to owner; no other board exists.
type fakeBoards struct {
	owner   string
	revoked []string
}

func (*fakeBoards) CreateBoard(_ context.Context, guestID, title string) (access.Board, error) {
	return access.Board{ID: "new-" + guestID, Title: title, Visibility: "private"}, nil
}

func (*fakeBoards) ListBoards(context.Context, string) ([]access.Board, error) {
	return []access.Board{}, nil
}

func (f *fakeBoards) CreateLink(_ context.Context, boardID, guestID string, role pb.Role) (access.Link, error) {
	if err := f.owns(boardID, guestID); err != nil {
		return access.Link{}, err
	}
	return access.Link{ID: "l1", Token: "tok", Role: access.RoleName(role)}, nil
}

func (f *fakeBoards) ListLinks(_ context.Context, boardID, guestID string) ([]access.Link, error) {
	return []access.Link{}, f.owns(boardID, guestID)
}

func (f *fakeBoards) RevokeLink(_ context.Context, boardID, guestID, linkID string) error {
	if err := f.owns(boardID, guestID); err != nil {
		return err
	}
	f.revoked = append(f.revoked, linkID)
	return nil
}

func (f *fakeBoards) owns(boardID, guestID string) error {
	switch {
	case boardID != "b1":
		return access.ErrNotFound
	case guestID != f.owner:
		return access.ErrNotOwner
	}
	return nil
}

func TestBoardAPI(t *testing.T) {
	signer := access.NewSigner([]byte("test"))
	fb := &fakeBoards{}
	var kicked []string
	h := NewRouter(Deps{
		Gateway:  http.NotFoundHandler(),
		Metrics:  metrics.New(),
		Ready:    func(context.Context) error { return nil },
		Signer:   signer,
		Boards:   fb,
		KickLink: func(b, l string) { kicked = append(kicked, b+"/"+l) },
	})
	do := func(method, path, token, body string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	code, body := do("POST", "/api/guest", "", "")
	var guest struct{ Token, GuestID string }
	if code != http.StatusCreated || json.Unmarshal([]byte(body), &guest) != nil {
		t.Fatalf("POST /api/guest = %d %s", code, body)
	}
	if id, ok := signer.Verify(guest.Token); !ok || id != guest.GuestID {
		t.Fatalf("issued token does not verify: %+v", guest)
	}

	if code, _ := do("GET", "/api/boards", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := do("GET", "/api/boards", guest.Token+"x", ""); code != http.StatusUnauthorized {
		t.Fatalf("forged token: %d", code)
	}
	code, body = do("POST", "/api/boards", guest.Token, `{"title":"Plans"}`)
	if code != http.StatusCreated || !strings.Contains(body, `"id":"new-`+guest.GuestID+`"`) || !strings.Contains(body, `"title":"Plans"`) {
		t.Fatalf("create board = %d %s", code, body)
	}
	if code, _ := do("POST", "/api/boards", guest.Token, ""); code != http.StatusCreated {
		t.Fatalf("create board without a body = %d", code)
	}

	ownerToken, ownerID := signer.Issue()
	fb.owner = ownerID
	if code, _ := do("POST", "/api/boards/b1/links", ownerToken, `{"role":"owner"}`); code != http.StatusBadRequest {
		t.Fatalf("owner-role link: %d", code)
	}
	if code, body := do("POST", "/api/boards/b1/links", ownerToken, `{"role":"viewer"}`); code != http.StatusCreated || !strings.Contains(body, `"token":"tok"`) {
		t.Fatalf("create link = %d %s", code, body)
	}
	if code, _ := do("POST", "/api/boards/b1/links", guest.Token, `{"role":"viewer"}`); code != http.StatusForbidden {
		t.Fatalf("non-owner create link: %d", code)
	}
	if code, _ := do("GET", "/api/boards/nope/links", ownerToken, ""); code != http.StatusNotFound {
		t.Fatalf("unknown board: %d", code)
	}
	if code, _ := do("DELETE", "/api/boards/b1/links/l1", guest.Token, ""); code != http.StatusForbidden || len(kicked) != 0 {
		t.Fatalf("non-owner revoke: %d, kicked %v", code, kicked)
	}
	if code, _ := do("DELETE", "/api/boards/b1/links/l1", ownerToken, ""); code != http.StatusNoContent {
		t.Fatalf("revoke: %d", code)
	}
	if len(fb.revoked) != 1 || len(kicked) != 1 || kicked[0] != "b1/l1" {
		t.Fatalf("revoked %v, kicked %v", fb.revoked, kicked)
	}
}

func TestBoardCreationIsLimitedPerIP(t *testing.T) {
	signer := access.NewSigner([]byte("test"))
	h := NewRouter(Deps{
		Gateway:      http.NotFoundHandler(),
		Metrics:      metrics.New(),
		Ready:        func(context.Context) error { return nil },
		Signer:       signer,
		Boards:       &fakeBoards{},
		BoardCreates: ratelimit.NewKeyed(0, 2),
	})
	token, _ := signer.Issue()
	create := func(ip string) int {
		req := httptest.NewRequest("POST", "/api/boards", nil)
		req.RemoteAddr = ip + ":1234"
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	for i, want := range []int{201, 201, 429} {
		if got := create("1.1.1.1"); got != want {
			t.Fatalf("create %d = %d, want %d", i, got, want)
		}
	}
	if got := create("2.2.2.2"); got != 201 {
		t.Fatalf("another address = %d", got)
	}
}
