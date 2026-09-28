// Package access decides who may do what on a board (docs/ARCHITECTURE.md,
// "Auth, permissions, abuse"):
//   - Guests get a signed identity with no signup (Signer).
//   - Boards reached by visiting an unknown id are public: anyone can edit.
//   - Boards created through the API are private: their owner, and holders
//     of a share link (view or edit), may open them. Links are stored only
//     as hashes and can be revoked.
package access

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"

	pb "whiteboard/internal/pb/whiteboard/v1"
)

var (
	// ErrForbidden: no access to the board.
	ErrForbidden = errors.New("forbidden")
	// ErrNotOwner: only the board's owner may do this.
	ErrNotOwner = errors.New("not the board owner")
	// ErrNotFound: no such board or link.
	ErrNotFound = errors.New("not found")
)

// Grant is what a connection may do on a board.
type Grant struct {
	Role pb.Role
	// LinkID is the share link that granted access, if any, so revoking
	// the link can disconnect its users.
	LinkID string
}

// CanEdit reports whether the grant allows changing the board.
func (g Grant) CanEdit() bool { return g.Role >= pb.Role_ROLE_EDITOR }

// Authorizer decides access when a client joins a board.
type Authorizer interface {
	Authorize(ctx context.Context, boardID, guestID, shareToken string) (Grant, error)
}

// Open lets everyone edit every board (tests and single-user setups).
type Open struct{}

func (Open) Authorize(context.Context, string, string, string) (Grant, error) {
	return Grant{Role: pb.Role_ROLE_EDITOR}, nil
}

// Signer issues and checks guest tokens: "<guest id>.<HMAC-SHA256 of it>".
// A guest id is a stable identity with no account behind it.
type Signer struct{ key []byte }

func NewSigner(secret []byte) *Signer { return &Signer{key: secret} }

// Issue creates a new guest identity.
func (s *Signer) Issue() (token, guestID string) {
	guestID = randomID(16)
	return guestID + "." + s.mac(guestID), guestID
}

// Verify returns the guest id of a valid token.
func (s *Signer) Verify(token string) (string, bool) {
	id, sig, ok := strings.Cut(token, ".")
	if !ok || id == "" || subtle.ConstantTimeCompare([]byte(sig), []byte(s.mac(id))) != 1 {
		return "", false
	}
	return id, true
}

func (s *Signer) mac(id string) string {
	h := hmac.New(sha256.New, s.key)
	h.Write([]byte(id))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// randomID returns n random bytes as URL-safe base64, valid as a board id.
func randomID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// hashToken is what is stored for a share link token.
func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// RoleName is the API name of a link role.
func RoleName(r pb.Role) string {
	switch r {
	case pb.Role_ROLE_VIEWER:
		return "viewer"
	case pb.Role_ROLE_EDITOR:
		return "editor"
	case pb.Role_ROLE_OWNER:
		return "owner"
	default:
		return ""
	}
}

// ParseRole parses a link role ("viewer" or "editor").
func ParseRole(s string) (pb.Role, bool) {
	switch s {
	case "viewer":
		return pb.Role_ROLE_VIEWER, true
	case "editor":
		return pb.Role_ROLE_EDITOR, true
	}
	return 0, false
}
