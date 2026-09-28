package access_test

import (
	"context"
	"errors"
	"testing"

	"whiteboard/internal/access"
	"whiteboard/internal/db"
	"whiteboard/internal/db/dbtest"
	pb "whiteboard/internal/pb/whiteboard/v1"
)

func TestSigner(t *testing.T) {
	s := access.NewSigner([]byte("secret"))
	token, id := s.Issue()
	if got, ok := s.Verify(token); !ok || got != id {
		t.Fatalf("Verify(own token) = %q, %v", got, ok)
	}
	for _, bad := range []string{"", id, id + ".x", "other." + token[len(id)+1:], token + "x"} {
		if _, ok := s.Verify(bad); ok {
			t.Fatalf("Verify(%q) accepted", bad)
		}
	}
	if _, ok := access.NewSigner([]byte("other")).Verify(token); ok {
		t.Fatal("token accepted under another secret")
	}
}

func TestBoardsAndLinks(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := access.NewPostgres(pool)
	grant := func(board, guest, token string) (pb.Role, error) {
		g, err := s.Authorize(ctx, board, guest, token)
		return g.Role, err
	}

	// Unknown ids become public boards anyone can edit.
	if r, err := grant("demo", "", ""); err != nil || r != pb.Role_ROLE_EDITOR {
		t.Fatalf("public board: %v, %v", r, err)
	}

	// A private board: owner yes, others no.
	b, err := s.CreateBoard(ctx, "alice", "Plans")
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := grant(b.ID, "alice", ""); r != pb.Role_ROLE_OWNER {
		t.Fatalf("owner role = %v", r)
	}
	if _, err := grant(b.ID, "bob", ""); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("stranger: %v, want ErrForbidden", err)
	}

	// Links: only the owner can make them; they grant their role until revoked.
	if _, err := s.CreateLink(ctx, b.ID, "bob", pb.Role_ROLE_EDITOR); !errors.Is(err, access.ErrNotOwner) {
		t.Fatalf("non-owner created a link: %v", err)
	}
	view, err := s.CreateLink(ctx, b.ID, "alice", pb.Role_ROLE_VIEWER)
	if err != nil {
		t.Fatal(err)
	}
	edit, _ := s.CreateLink(ctx, b.ID, "alice", pb.Role_ROLE_EDITOR)
	if g, _ := s.Authorize(ctx, b.ID, "bob", view.Token); g.Role != pb.Role_ROLE_VIEWER || g.LinkID != view.ID {
		t.Fatalf("view link grant = %+v", g)
	}
	if r, _ := grant(b.ID, "", edit.Token); r != pb.Role_ROLE_EDITOR {
		t.Fatalf("edit link role = %v", r)
	}
	if _, err := grant("demo", "", view.Token); err != nil {
		t.Fatalf("a link for another board must not matter on a public one: %v", err)
	}
	if _, err := grant(b.ID, "", view.Token+"x"); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("wrong token: %v", err)
	}

	links, _ := s.ListLinks(ctx, b.ID, "alice")
	if len(links) != 2 || links[0].Token != "" {
		t.Fatalf("links = %+v (tokens must not be listed)", links)
	}
	if err := s.RevokeLink(ctx, b.ID, "alice", view.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := grant(b.ID, "bob", view.Token); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("revoked link still works: %v", err)
	}
	if err := s.RevokeLink(ctx, b.ID, "alice", view.ID); !errors.Is(err, access.ErrNotFound) {
		t.Fatalf("second revoke: %v", err)
	}

	boards, _ := s.ListBoards(ctx, "alice")
	if len(boards) != 1 || boards[0].ID != b.ID || boards[0].Title != "Plans" {
		t.Fatalf("alice's boards = %+v", boards)
	}
	if boards, _ := s.ListBoards(ctx, "bob"); len(boards) != 0 {
		t.Fatalf("bob's boards = %+v", boards)
	}
}
