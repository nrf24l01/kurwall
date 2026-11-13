// Package auth contains password, token and identity primitives shared by the
// coordinator HTTP layer, services and the users CLI.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"

	KindToken   = "token"
	KindSession = "session"

	SessionCookie = "kurwall_session"
	CSRFCookie    = "kurwall_csrf"
	CSRFHeader    = "X-CSRF-Token"

	SessionTTL = 12 * time.Hour

	MinPasswordLength = 12
	MaxPasswordLength = 72
)

var (
	// ErrUnauthorized means the caller must sign in or present an API token.
	ErrUnauthorized = errors.New("authentication required")

	usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,63}$`)
	bcryptCost      = 12
	dummyHash       string
)

func init() {
	hash, err := bcrypt.GenerateFromPassword([]byte("kurwall-password-timing-value"), bcryptCost)
	if err == nil {
		dummyHash = string(hash)
	}
}

// Identity is the authenticated caller attached to a request context.
type Identity struct {
	UserID    string
	Username  string
	Role      string
	Kind      string
	SessionID string
	CSRFHash  []byte
	ExpiresAt time.Time
}

// IsAdmin reports whether the identity may mutate configuration.
func (i Identity) IsAdmin() bool { return i.Role == RoleAdmin }

type identityKey struct{}

// WithIdentity stores the identity in a request context.
func WithIdentity(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, identity)
}

// IdentityFrom reads the identity stored by the auth middleware.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(identityKey{}).(Identity)
	return identity, ok
}

// NormalizeUsername lowercases and trims a username.
func NormalizeUsername(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// ValidateUsername enforces the account name format.
func ValidateUsername(value string) error {
	if !usernamePattern.MatchString(NormalizeUsername(value)) {
		return errors.New("username must be 3-64 characters: letters, digits, dots, underscores or dashes")
	}
	return nil
}

// ValidatePassword enforces the password length accepted by bcrypt.
func ValidatePassword(value string) error {
	if len(value) < MinPasswordLength {
		return errors.New("password must be at least 12 characters")
	}
	if len(value) > MaxPasswordLength {
		return errors.New("password must be at most 72 bytes")
	}
	return nil
}

// HashPassword returns a bcrypt hash.
func HashPassword(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// VerifyPassword compares a password with a bcrypt hash.
func VerifyPassword(hash, password string) bool {
	if hash == "" {
		hash = dummyHash
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// DummyPasswordCheck burns the same time as a real comparison for unknown users.
func DummyPasswordCheck(password string) {
	if dummyHash != "" {
		_ = bcrypt.CompareHashAndPassword([]byte(dummyHash), []byte(password))
	}
}

// NewToken returns a URL-safe random token and its SHA-256 hash.
func NewToken() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	return token, HashToken(token), nil
}

// HashToken hashes a presented token for constant-time database lookup.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// ValidTokenFormat rejects tokens that could not have been issued by NewToken.
func ValidTokenFormat(token string) bool {
	if len(token) < 20 || len(token) > 128 {
		return false
	}
	for _, char := range token {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}
