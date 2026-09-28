package access

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	pb "whiteboard/internal/pb/whiteboard/v1"
)

// Postgres stores board ownership and share links.
type Postgres struct{ pool *pgxpool.Pool }

var _ Authorizer = (*Postgres)(nil)

func NewPostgres(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Authorize applies the access rules. Visiting an unknown board id creates
// it as a public board, as before W6.
func (s *Postgres) Authorize(ctx context.Context, boardID, guestID, shareToken string) (Grant, error) {
	var visibility string
	var owner *string
	err := s.pool.QueryRow(ctx, "SELECT visibility, owner_id FROM boards WHERE id = $1", boardID).Scan(&visibility, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := s.pool.Exec(ctx, "INSERT INTO boards (id) VALUES ($1) ON CONFLICT DO NOTHING", boardID); err != nil {
			return Grant{}, err
		}
		return Grant{Role: pb.Role_ROLE_EDITOR}, nil
	}
	if err != nil {
		return Grant{}, err
	}
	if guestID != "" && owner != nil && *owner == guestID {
		return Grant{Role: pb.Role_ROLE_OWNER}, nil
	}
	if shareToken != "" {
		var id, role string
		err := s.pool.QueryRow(ctx,
			"SELECT id, role FROM share_links WHERE token_hash = $1 AND board_id = $2 AND revoked_at IS NULL",
			hashToken(shareToken), boardID).Scan(&id, &role)
		if err == nil {
			r, _ := ParseRole(role)
			if visibility == "public" && r < pb.Role_ROLE_EDITOR {
				r = pb.Role_ROLE_EDITOR // a public board is editable anyway
			}
			return Grant{Role: r, LinkID: id}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return Grant{}, err
		}
	}
	if visibility == "public" {
		return Grant{Role: pb.Role_ROLE_EDITOR}, nil
	}
	return Grant{}, ErrForbidden
}

type Board struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	Visibility string    `json:"visibility"`
	CreatedAt  time.Time `json:"createdAt"`
}

// CreateBoard creates a private board owned by guestID.
func (s *Postgres) CreateBoard(ctx context.Context, guestID, title string) (Board, error) {
	b := Board{ID: randomID(9), Title: title, Visibility: "private"}
	err := s.pool.QueryRow(ctx,
		"INSERT INTO boards (id, title, visibility, owner_id) VALUES ($1, $2, 'private', $3) RETURNING created_at",
		b.ID, title, guestID).Scan(&b.CreatedAt)
	return b, err
}

// ListBoards returns the boards guestID owns, newest first.
func (s *Postgres) ListBoards(ctx context.Context, guestID string) ([]Board, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT id, title, visibility, created_at FROM boards WHERE owner_id = $1 ORDER BY created_at DESC LIMIT 200", guestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Board{}
	for rows.Next() {
		var b Board
		if err := rows.Scan(&b.ID, &b.Title, &b.Visibility, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

type Link struct {
	ID string `json:"id"`
	// Token is only set when the link is created; it is not stored.
	Token     string     `json:"token,omitempty"`
	Role      string     `json:"role"`
	CreatedAt time.Time  `json:"createdAt"`
	RevokedAt *time.Time `json:"revokedAt,omitempty"`
}

func (s *Postgres) requireOwner(ctx context.Context, boardID, guestID string) error {
	var owner *string
	err := s.pool.QueryRow(ctx, "SELECT owner_id FROM boards WHERE id = $1", boardID).Scan(&owner)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return err
	case guestID == "" || owner == nil || *owner != guestID:
		return ErrNotOwner
	}
	return nil
}

// CreateLink makes a share link; the returned token is shown once.
func (s *Postgres) CreateLink(ctx context.Context, boardID, guestID string, role pb.Role) (Link, error) {
	if err := s.requireOwner(ctx, boardID, guestID); err != nil {
		return Link{}, err
	}
	name := RoleName(role)
	if role != pb.Role_ROLE_VIEWER && role != pb.Role_ROLE_EDITOR {
		return Link{}, fmt.Errorf("invalid link role %v", role)
	}
	l := Link{ID: randomID(9), Token: randomID(24), Role: name}
	err := s.pool.QueryRow(ctx,
		"INSERT INTO share_links (id, board_id, token_hash, role) VALUES ($1, $2, $3, $4) RETURNING created_at",
		l.ID, boardID, hashToken(l.Token), name).Scan(&l.CreatedAt)
	return l, err
}

// ListLinks returns a board's links (without tokens).
func (s *Postgres) ListLinks(ctx context.Context, boardID, guestID string) ([]Link, error) {
	if err := s.requireOwner(ctx, boardID, guestID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx,
		"SELECT id, role, created_at, revoked_at FROM share_links WHERE board_id = $1 ORDER BY created_at", boardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Link{}
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.ID, &l.Role, &l.CreatedAt, &l.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// RevokeLink stops a link from granting access.
func (s *Postgres) RevokeLink(ctx context.Context, boardID, guestID, linkID string) error {
	if err := s.requireOwner(ctx, boardID, guestID); err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx,
		"UPDATE share_links SET revoked_at = now() WHERE id = $1 AND board_id = $2 AND revoked_at IS NULL", linkID, boardID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
