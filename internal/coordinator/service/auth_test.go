package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/kurwall/kurwall/internal/coordinator/auth"
	"github.com/kurwall/kurwall/internal/coordinator/domain"
)

type fakeAuthRepo struct {
	count int
}

func (f *fakeAuthRepo) CountUsers(context.Context) (int, error) { return f.count, nil }
func (f *fakeAuthRepo) GetUserByUsername(context.Context, string) (domain.User, bool, error) {
	return domain.User{}, false, nil
}
func (f *fakeAuthRepo) CreateSession(context.Context, domain.Session) error { return nil }
func (f *fakeAuthRepo) GetSessionByTokenHash(context.Context, []byte) (domain.Session, domain.User, bool, error) {
	return domain.Session{}, domain.User{}, false, nil
}
func (f *fakeAuthRepo) TouchSession(context.Context, string, time.Time) error { return nil }
func (f *fakeAuthRepo) DeleteSessionByTokenHash(context.Context, []byte) error {
	return nil
}
func (f *fakeAuthRepo) DeleteExpiredSessions(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func TestAuthNeverOpensWithoutAccounts(t *testing.T) {
	ctx := context.Background()
	repo := &fakeAuthRepo{count: 0}
	service := NewAuth(repo, "", zerolog.Nop())

	mode, err := service.Mode(ctx)
	if err != nil {
		t.Fatalf("Mode: %v", err)
	}
	if mode != "users" {
		t.Fatalf("Mode = %q, want users", mode)
	}
	setup, err := service.SetupRequired(ctx)
	if err != nil {
		t.Fatalf("SetupRequired: %v", err)
	}
	if !setup {
		t.Fatal("SetupRequired = false, want true")
	}
	if _, err := service.Authenticate(ctx, "", ""); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("Authenticate without credentials = %v, want ErrUnauthorized", err)
	}
}

func TestAuthTokenModeStillRequiresToken(t *testing.T) {
	ctx := context.Background()
	service := NewAuth(&fakeAuthRepo{count: 0}, "secret-token", zerolog.Nop())

	if _, err := service.Authenticate(ctx, "", ""); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("Authenticate without token = %v, want ErrUnauthorized", err)
	}
	identity, err := service.Authenticate(ctx, "secret-token", "")
	if err != nil {
		t.Fatalf("Authenticate with token: %v", err)
	}
	if identity.Kind != auth.KindToken || identity.Role != auth.RoleAdmin {
		t.Fatalf("identity = %+v, want admin token", identity)
	}
	setup, err := service.SetupRequired(ctx)
	if err != nil {
		t.Fatalf("SetupRequired: %v", err)
	}
	if setup {
		t.Fatal("SetupRequired = true in token mode, want false")
	}
}

func TestAuthUsersModeRequiresSession(t *testing.T) {
	ctx := context.Background()
	service := NewAuth(&fakeAuthRepo{count: 1}, "", zerolog.Nop())

	if _, err := service.Authenticate(ctx, "", ""); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("Authenticate without session = %v, want ErrUnauthorized", err)
	}
	setup, err := service.SetupRequired(ctx)
	if err != nil {
		t.Fatalf("SetupRequired: %v", err)
	}
	if setup {
		t.Fatal("SetupRequired = true with an account, want false")
	}
}
