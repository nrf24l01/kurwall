package service

import (
	"context"
	"crypto/subtle"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/kurwall/kurwall/internal/coordinator/auth"
	"github.com/kurwall/kurwall/internal/coordinator/domain"
	"github.com/kurwall/kurwall/internal/platform/apperr"
	"github.com/kurwall/kurwall/internal/platform/id"
)

// ErrRateLimited is returned after too many failed sign-in attempts.
var ErrRateLimited = errors.New("too many sign-in attempts")

// AuthRepository is the persistence surface needed for accounts and sessions.
type AuthRepository interface {
	CountUsers(ctx context.Context) (int, error)
	GetUserByUsername(ctx context.Context, username string) (domain.User, bool, error)
	CreateSession(ctx context.Context, session domain.Session) error
	GetSessionByTokenHash(ctx context.Context, tokenHash []byte) (domain.Session, domain.User, bool, error)
	TouchSession(ctx context.Context, sessionID string, now time.Time) error
	DeleteSessionByTokenHash(ctx context.Context, tokenHash []byte) error
	DeleteExpiredSessions(ctx context.Context, before time.Time) (int64, error)
}

type attemptWindow struct {
	count int
	reset time.Time
}

// Auth authenticates dashboard sessions and API tokens.
type Auth struct {
	repo        AuthRepository
	staticToken string
	logger      zerolog.Logger
	sessionTTL  time.Duration

	mu       sync.Mutex
	attempts map[string]attemptWindow
}

// NewAuth builds the authentication service.
func NewAuth(repo AuthRepository, staticToken string, logger zerolog.Logger) *Auth {
	return &Auth{
		repo: repo, staticToken: staticToken, logger: logger,
		sessionTTL: auth.SessionTTL, attempts: map[string]attemptWindow{},
	}
}

// Mode reports how callers authenticate: users, token or open.
func (s *Auth) Mode(ctx context.Context) (string, error) {
	count, err := s.repo.CountUsers(ctx)
	if err != nil {
		return "", err
	}
	if count > 0 {
		return "users", nil
	}
	if s.staticToken != "" {
		return "token", nil
	}
	return "open", nil
}

// Required reports whether unauthenticated callers are rejected.
func (s *Auth) Required(ctx context.Context) (bool, error) {
	mode, err := s.Mode(ctx)
	if err != nil {
		return true, err
	}
	return mode != "open", nil
}

// Authenticate resolves a bearer token or session cookie.
func (s *Auth) Authenticate(ctx context.Context, bearer, sessionToken string) (auth.Identity, error) {
	if bearer != "" && s.staticToken != "" &&
		subtle.ConstantTimeCompare([]byte(bearer), []byte(s.staticToken)) == 1 {
		return auth.Identity{Username: "api-token", Role: auth.RoleAdmin, Kind: auth.KindToken}, nil
	}
	if sessionToken != "" {
		session, user, found, err := s.repo.GetSessionByTokenHash(ctx, auth.HashToken(sessionToken))
		if err != nil {
			return auth.Identity{}, err
		}
		if found && !user.Disabled && time.Now().UTC().Before(session.ExpiresAt) {
			_ = s.repo.TouchSession(ctx, session.ID, time.Now().UTC())
			return auth.Identity{
				UserID: user.ID, Username: user.Username, Role: user.Role, Kind: auth.KindSession,
				SessionID: session.ID, CSRFHash: session.CSRFHash, ExpiresAt: session.ExpiresAt,
			}, nil
		}
	}
	required, err := s.Required(ctx)
	if err != nil {
		return auth.Identity{}, err
	}
	if required {
		return auth.Identity{}, auth.ErrUnauthorized
	}
	return auth.Identity{Username: "operator", Role: auth.RoleAdmin, Kind: auth.KindOpen}, nil
}

// CheckCSRF verifies the double-submitted token for cookie sessions.
func (s *Auth) CheckCSRF(identity auth.Identity, token string) bool {
	if identity.Kind != auth.KindSession {
		return true
	}
	if token == "" {
		return false
	}
	hash := auth.HashToken(token)
	return subtle.ConstantTimeCompare(hash, identity.CSRFHash) == 1
}

// Login verifies a password and creates a session.
func (s *Auth) Login(ctx context.Context, username, password, userAgent, remoteIP string) (string, string, domain.User, error) {
	username = auth.NormalizeUsername(username)
	key := remoteIP + "|" + username
	if !s.allowAttempt(key) {
		return "", "", domain.User{}, ErrRateLimited
	}

	user, found, err := s.repo.GetUserByUsername(ctx, username)
	if err != nil {
		return "", "", domain.User{}, err
	}
	if !found {
		auth.DummyPasswordCheck(password)
		s.recordFailure(key)
		return "", "", domain.User{}, auth.ErrUnauthorized
	}
	if user.Disabled {
		auth.DummyPasswordCheck(password)
		s.recordFailure(key)
		return "", "", domain.User{}, auth.ErrUnauthorized
	}
	if !auth.VerifyPassword(user.PasswordHash, password) {
		s.recordFailure(key)
		return "", "", domain.User{}, auth.ErrUnauthorized
	}
	s.resetAttempts(key)

	sessionToken, sessionHash, err := auth.NewToken()
	if err != nil {
		return "", "", domain.User{}, apperr.Internal("generate session token", err)
	}
	csrfToken, csrfHash, err := auth.NewToken()
	if err != nil {
		return "", "", domain.User{}, apperr.Internal("generate csrf token", err)
	}
	now := time.Now().UTC()
	session := domain.Session{
		ID: id.New(), UserID: user.ID, TokenHash: sessionHash, CSRFHash: csrfHash,
		CreatedAt: now, ExpiresAt: now.Add(s.sessionTTL), LastSeenAt: now,
		UserAgent: truncate(userAgent, 300), RemoteIP: truncate(remoteIP, 64),
	}
	if err := s.repo.CreateSession(ctx, session); err != nil {
		return "", "", domain.User{}, err
	}
	if removed, err := s.repo.DeleteExpiredSessions(ctx, now); err == nil && removed > 0 {
		s.logger.Debug().Int64("sessions", removed).Msg("removed expired sessions")
	}
	return sessionToken, csrfToken, user, nil
}

// Logout deletes the session behind a cookie.
func (s *Auth) Logout(ctx context.Context, sessionToken string) {
	if sessionToken == "" {
		return
	}
	if err := s.repo.DeleteSessionByTokenHash(ctx, auth.HashToken(sessionToken)); err != nil {
		s.logger.Warn().Err(err).Msg("failed to delete session")
	}
}

func (s *Auth) allowAttempt(key string) bool {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	window, ok := s.attempts[key]
	if !ok || now.After(window.reset) {
		return true
	}
	return window.count < 5
}

func (s *Auth) recordFailure(key string) {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	window, ok := s.attempts[key]
	if !ok || now.After(window.reset) {
		s.attempts[key] = attemptWindow{count: 1, reset: now.Add(10 * time.Minute)}
		return
	}
	window.count++
	s.attempts[key] = window
	if len(s.attempts) > 5000 {
		for candidate, entry := range s.attempts {
			if now.After(entry.reset) {
				delete(s.attempts, candidate)
			}
		}
	}
}

func (s *Auth) resetAttempts(key string) {
	s.mu.Lock()
	delete(s.attempts, key)
	s.mu.Unlock()
}

func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
