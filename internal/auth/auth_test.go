package auth

import (
	"context"
	"encoding/base32"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mostlygeek/llama-swap/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testPassword = "correct horse battery"

// fakeRepo is an in-memory store.AuthRepository.
type fakeRepo struct {
	mu       sync.Mutex
	admin    *store.AdminAccount
	recovery map[string]bool
	sessions map[string]store.AdminSession
	keys     []store.APIKey
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{recovery: map[string]bool{}, sessions: map[string]store.AdminSession{}}
}

func (r *fakeRepo) Admin(context.Context) (store.AdminAccount, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.admin == nil {
		return store.AdminAccount{}, false, nil
	}
	return *r.admin, true, nil
}

func (r *fakeRepo) CreateAdmin(_ context.Context, a store.AdminAccount) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.admin != nil {
		return false, nil
	}
	r.admin = &a
	return true, nil
}

func (r *fakeRepo) UpdateAdminPassword(_ context.Context, hash string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.admin.PasswordHash = hash
	r.admin.UpdatedAt = at
	return nil
}

func (r *fakeRepo) SetPendingTOTPSecret(_ context.Context, secret string, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.admin.TOTPSecret = secret
	r.admin.TOTPEnabled = false
	return nil
}

func (r *fakeRepo) EnableTOTP(_ context.Context, lastStep int64, codeHashes []string, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.admin.TOTPEnabled = true
	r.admin.TOTPLastStep = lastStep
	r.recovery = map[string]bool{}
	for _, h := range codeHashes {
		r.recovery[h] = true
	}
	return nil
}

func (r *fakeRepo) UpdateTOTPLastStep(_ context.Context, step int64, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.admin.TOTPLastStep = step
	return nil
}

func (r *fakeRepo) DisableTOTP(context.Context, time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.admin.TOTPSecret = ""
	r.admin.TOTPEnabled = false
	r.admin.TOTPLastStep = 0
	r.recovery = map[string]bool{}
	return nil
}

func (r *fakeRepo) ConsumeRecoveryCode(_ context.Context, h string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.recovery[h] {
		return false, nil
	}
	delete(r.recovery, h)
	return true, nil
}

func (r *fakeRepo) DeleteAdmin(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.admin = nil
	r.recovery = map[string]bool{}
	r.sessions = map[string]store.AdminSession{}
	return nil
}

func (r *fakeRepo) CreateSession(_ context.Context, s store.AdminSession) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[s.TokenHash] = s
	return nil
}

func (r *fakeRepo) Session(_ context.Context, h string) (store.AdminSession, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[h]
	return s, ok, nil
}

func (r *fakeRepo) TouchSession(_ context.Context, h string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.sessions[h]
	s.LastSeenAt = at
	r.sessions[h] = s
	return nil
}

func (r *fakeRepo) DeleteSession(_ context.Context, h string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, h)
	return nil
}

func (r *fakeRepo) DeleteSessionsExcept(_ context.Context, keep string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for h := range r.sessions {
		if h != keep {
			delete(r.sessions, h)
		}
	}
	return nil
}

func (r *fakeRepo) PruneSessions(_ context.Context, now time.Time, idle time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for h, s := range r.sessions {
		if !now.Before(s.ExpiresAt) || !now.Before(s.LastSeenAt.Add(idle)) {
			delete(r.sessions, h)
		}
	}
	return nil
}

func (r *fakeRepo) CreateAPIKey(_ context.Context, k store.APIKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys = append(r.keys, k)
	return nil
}

func (r *fakeRepo) APIKeys(context.Context) ([]store.APIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]store.APIKey(nil), r.keys...), nil
}

func (r *fakeRepo) RevokeAPIKey(_ context.Context, id string, at time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.keys {
		if r.keys[i].ID == id && r.keys[i].RevokedAt == nil {
			r.keys[i].RevokedAt = &at
			return true, nil
		}
	}
	return false, nil
}

func (r *fakeRepo) TouchAPIKey(context.Context, string, time.Time) error { return nil }

// clock is a settable time source for Options.Now.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTestService(t *testing.T, repo *fakeRepo, c *clock, opts Options) *Service {
	t.Helper()
	opts.Now = c.now
	svc, err := New(context.Background(), repo, opts)
	require.NoError(t, err)
	return svc
}

// setUp completes the setup flow and returns the service and the setup token.
func setUp(t *testing.T, svc *Service) {
	t.Helper()
	token, ok := svc.SetupToken()
	require.True(t, ok)
	_, err := svc.Setup(context.Background(), "1.2.3.4", token, "admin", testPassword)
	require.NoError(t, err)
}

func TestAuth_SetupTokenSingleUse(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	svc := newTestService(t, newFakeRepo(), c, Options{})
	token, ok := svc.SetupToken()
	require.True(t, ok)

	tests := []struct {
		name    string
		token   string
		wantErr error
	}{
		{"wrong token rejected", "nope", ErrSetupTokenInvalid},
		{"right token accepted", token, nil},
		{"token reused after setup", token, ErrAlreadySetUp},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.Setup(context.Background(), "1.2.3.4", tt.token, "admin", testPassword)
			if tt.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}

	_, ok = svc.SetupToken()
	assert.False(t, ok, "setup token must be cleared after setup")
}

func TestAuth_SetupRejectsShortPassword(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	svc := newTestService(t, newFakeRepo(), c, Options{})
	token, _ := svc.SetupToken()
	_, err := svc.Setup(context.Background(), "1.2.3.4", token, "admin", "short")
	assert.ErrorIs(t, err, ErrPasswordTooShort)
	assert.False(t, svc.AdminConfigured())
}

func TestAuth_LoginWithoutTwoFactor(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	svc := newTestService(t, newFakeRepo(), c, Options{})
	setUp(t, svc)

	tests := []struct {
		name     string
		username string
		password string
		wantErr  error
	}{
		{"correct credentials", "admin", testPassword, nil},
		{"wrong password", "admin", "wrong password here", ErrInvalidCredentials},
		{"wrong username", "root", testPassword, ErrInvalidCredentials},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, err := svc.Login(context.Background(), "5.6.7.8", tt.username, tt.password, "")
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			_, ok := svc.Session(context.Background(), token)
			assert.True(t, ok)
		})
	}
}

func TestAuth_LoginWithTwoFactorAndRecoveryCodes(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	svc := newTestService(t, newFakeRepo(), c, Options{})
	setUp(t, svc)
	ctx := context.Background()

	enroll, err := svc.BeginTwoFactor(ctx)
	require.NoError(t, err)
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enroll.Secret)
	require.NoError(t, err)
	code := totpCode(raw, c.t.Unix()/totpPeriod)
	codes, err := svc.ConfirmTwoFactor(ctx, "1.2.3.4", code)
	require.NoError(t, err)
	require.Len(t, codes, recoveryCodeCount)

	// The step used for confirmation is recorded, so the same code cannot log in.
	_, err = svc.Login(ctx, "5.6.7.8", "admin", testPassword, code)
	assert.ErrorIs(t, err, ErrInvalidCredentials)

	// Next step: a fresh TOTP code works.
	c.t = c.t.Add(30 * time.Second)
	next := totpCode(raw, c.t.Unix()/totpPeriod)

	tests := []struct {
		name    string
		code    string
		wantErr error
	}{
		{"password without code", "", ErrTOTPRequired},
		{"bad code", "000000", ErrInvalidCredentials},
		{"totp code", next, nil},
		{"recovery code", codes[0], nil},
		{"recovery code reused", codes[0], ErrInvalidCredentials},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.Login(ctx, "5.6.7.8", "admin", testPassword, tt.code)
			if tt.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestAuth_SessionExpiry(t *testing.T) {
	const idle = time.Hour
	const absolute = 2 * time.Hour
	tests := []struct {
		name  string
		steps []time.Duration // advance before each Session check
		want  bool
	}{
		{"valid immediately", []time.Duration{0}, true},
		{"idle expiry", []time.Duration{idle + time.Second}, false},
		{"activity keeps idle session alive", []time.Duration{50 * time.Minute, 50 * time.Minute}, true},
		{"absolute expiry despite activity", []time.Duration{50 * time.Minute, 50 * time.Minute, 50 * time.Minute, 50 * time.Minute}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &clock{t: time.Unix(1_000_000, 0)}
			svc := newTestService(t, newFakeRepo(), c, Options{SessionIdle: idle, SessionAbsolute: absolute})
			setUp(t, svc)
			token, err := svc.Login(context.Background(), "5.6.7.8", "admin", testPassword, "")
			require.NoError(t, err)

			var ok bool
			for _, d := range tt.steps {
				c.t = c.t.Add(d)
				_, ok = svc.Session(context.Background(), token)
			}
			assert.Equal(t, tt.want, ok)
		})
	}
}

func TestAuth_ResetAdminRestartsSetup(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	repo := newFakeRepo()
	svc := newTestService(t, repo, c, Options{})
	setUp(t, svc)
	require.NoError(t, svc.ResetAdmin(context.Background()))

	assert.False(t, svc.AdminConfigured())
	token, ok := svc.SetupToken()
	require.True(t, ok)
	assert.NotEmpty(t, token)
}

func TestAuth_KeyRevocationAndResolve(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	svc := newTestService(t, newFakeRepo(), c, Options{})
	ctx := context.Background()

	plaintext, info, err := svc.CreateKey(ctx, "  ci  ", []string{"qwen"})
	require.NoError(t, err)
	assert.Equal(t, "ci", info.Name)
	assert.True(t, svc.KeysConfigured())

	tests := []struct {
		name      string
		presented string
		wantOK    bool
	}{
		{"active key resolves", plaintext, true},
		{"unknown key denied", KeyPrefix + "unknown", false},
		{"empty key denied", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := svc.ResolveKey(tt.presented)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, info.ID, got.ID)
				assert.Equal(t, []string{"qwen"}, got.Models)
			}
		})
	}

	require.NoError(t, svc.RevokeKey(ctx, info.ID))
	_, ok := svc.ResolveKey(plaintext)
	assert.False(t, ok, "revoked key must be denied immediately")
	assert.ErrorIs(t, svc.RevokeKey(ctx, info.ID), ErrKeyNotFound)
	assert.True(t, svc.KeysConfigured(), "revoked keys still count as configured")
}

func TestAuth_CreateKeyValidation(t *testing.T) {
	svc := newTestService(t, newFakeRepo(), &clock{t: time.Unix(1_000_000, 0)}, Options{})
	tests := []struct {
		name    string
		key     string
		models  []string
		wantErr error
	}{
		{"blank name", "   ", []string{"m"}, ErrKeyNameInvalid},
		{"name too long", string(make([]byte, 65)), []string{"m"}, ErrKeyNameInvalid},
		{"no models", "ok", nil, ErrKeyModelsRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := svc.CreateKey(context.Background(), tt.key, tt.models)
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestAuth_ThrottlesAfterRepeatedFailures(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	svc := newTestService(t, newFakeRepo(), c, Options{})
	setUp(t, svc)
	ctx := context.Background()

	for range throttleIPFailures {
		_, err := svc.Login(ctx, "9.9.9.9", "admin", "wrong password here", "")
		require.ErrorIs(t, err, ErrInvalidCredentials)
	}
	_, err := svc.Login(ctx, "9.9.9.9", "admin", testPassword, "")
	var throttled ThrottledError
	require.True(t, errors.As(err, &throttled), "locked address must be throttled even with the right password")
	assert.Equal(t, throttleIPBaseLock, throttled.RetryAfter)

	_, err = svc.Login(ctx, "8.8.8.8", "admin", testPassword, "")
	assert.NoError(t, err, "other addresses are not locked")
}
