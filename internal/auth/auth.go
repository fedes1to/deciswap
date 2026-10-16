// Package auth implements llama-swap's admin login, two-factor authentication,
// browser sessions and scoped API keys. Persistence is delegated to
// store.AuthRepository. This package owns the rules (hashing, expiry,
// throttling, TOTP) and the small in-memory caches the request path reads.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/mostlygeek/llama-swap/internal/store"
)

const (
	SessionCookieName = "llama_swap_session"
	CSRFHeader        = "X-Requested-With"
	CSRFHeaderValue   = "llama-swap"
	KeyPrefix         = "sk-ls-"
	MinPasswordLength = 12
)

const (
	maxPasswordLength      = 1024
	maxUsernameLength      = 64
	defaultSessionIdle     = 12 * time.Hour
	defaultSessionAbsolute = 7 * 24 * time.Hour
	sessionTouchInterval   = time.Minute
	setupTokenBytes        = 32
	sessionTokenBytes      = 32
)

var (
	ErrAlreadySetUp       = errors.New("admin already set up")
	ErrNotSetUp           = errors.New("admin not set up")
	ErrSetupTokenInvalid  = errors.New("setup token invalid")
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrTOTPRequired       = errors.New("two-factor code required")
	ErrTOTPInvalid        = errors.New("two-factor code invalid")
	ErrTOTPNotEnrolled    = errors.New("two-factor enrollment not started")
	ErrTOTPAlreadyEnabled = errors.New("two-factor already enabled")
	ErrTOTPNotEnabled     = errors.New("two-factor not enabled")
	ErrPasswordTooShort   = errors.New("password too short")
	ErrPasswordTooLong    = errors.New("password too long")
	ErrInvalidUsername    = errors.New("invalid username")
	ErrKeyNotFound        = errors.New("api key not found")
	ErrKeyNameInvalid     = errors.New("api key name must be 1-64 characters")
	ErrKeyModelsRequired  = errors.New("api key needs at least one model")
)

// ThrottledError is returned when an address or the global bucket is locked
// out of authentication attempts.
type ThrottledError struct{ RetryAfter time.Duration }

func (e ThrottledError) Error() string {
	return fmt.Sprintf("too many failed attempts, retry after %s", e.RetryAfter.Round(time.Second))
}

// Options configures a Service. Zero values select the defaults.
type Options struct {
	Now             func() time.Time // nil = time.Now
	SessionIdle     time.Duration    // 0 = 12h
	SessionAbsolute time.Duration    // 0 = 7*24h
}

// SessionInfo describes the admin behind a valid session.
type SessionInfo struct {
	Username         string
	TwoFactorEnabled bool
}

// Status describes the admin account for the login and setup screens.
type Status struct {
	SetupRequired    bool
	Username         string
	TwoFactorEnabled bool
}

// TwoFactorEnrollment is returned when two-factor enrollment begins.
type TwoFactorEnrollment struct {
	Secret string
	URI    string
}

// KeyInfo describes a scoped API key. It never includes the plaintext key.
type KeyInfo struct {
	ID, Name, Prefix string
	Models           []string // "*" alone means all models
	CreatedAt        time.Time
	LastUsedAt       *time.Time
	RevokedAt        *time.Time
}

// Service owns admin authentication state. All methods are safe for
// concurrent use.
type Service struct {
	repo            store.AuthRepository
	now             func() time.Time
	sessionIdle     time.Duration
	sessionAbsolute time.Duration
	throttle        throttle
	dummyHash       string // verified against when no admin exists, to flatten timing

	mu         sync.RWMutex
	admin      *store.AdminAccount
	setupToken string
	keys       map[string]KeyInfo // non-revoked keys by sha256 hex of the plaintext
	keysExist  bool               // any key row, including revoked
	touchMu    sync.Mutex
	keyTouched map[string]time.Time // last_used write time per key ID
}

// New loads the admin and key caches, prunes expired sessions, and generates
// a setup token when no admin exists.
func New(ctx context.Context, repo store.AuthRepository, opts Options) (*Service, error) {
	s := &Service{
		repo:            repo,
		now:             opts.Now,
		sessionIdle:     opts.SessionIdle,
		sessionAbsolute: opts.SessionAbsolute,
		keyTouched:      make(map[string]time.Time),
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.sessionIdle == 0 {
		s.sessionIdle = defaultSessionIdle
	}
	if s.sessionAbsolute == 0 {
		s.sessionAbsolute = defaultSessionAbsolute
	}

	account, found, err := repo.Admin(ctx)
	if err != nil {
		return nil, fmt.Errorf("load admin account: %w", err)
	}
	if found {
		s.admin = &account
	} else if s.setupToken, err = randomString(setupTokenBytes); err != nil {
		return nil, err
	}

	if err := repo.PruneSessions(ctx, s.now(), s.sessionIdle); err != nil {
		return nil, fmt.Errorf("prune sessions: %w", err)
	}
	if err := s.loadKeys(ctx); err != nil {
		return nil, err
	}

	dummy, err := randomString(16)
	if err != nil {
		return nil, err
	}
	if s.dummyHash, err = hashPassword(dummy); err != nil {
		return nil, fmt.Errorf("hash dummy password: %w", err)
	}
	return s, nil
}

// AdminConfigured reports whether an admin account exists. It does not hit the database.
func (s *Service) AdminConfigured() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.admin != nil
}

// SetupToken returns the one-time setup token. ok is false once an admin exists.
func (s *Service) SetupToken() (token string, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.admin != nil || s.setupToken == "" {
		return "", false
	}
	return s.setupToken, true
}

// Setup creates the admin account when the setup token matches, and returns a
// new session token. The setup token is cleared on success.
func (s *Service) Setup(ctx context.Context, ip, token, username, password string) (string, error) {
	now := s.now()
	if err := s.throttle.check(ip, now); err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.admin != nil {
		return "", ErrAlreadySetUp
	}
	if s.setupToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.setupToken)) != 1 {
		s.throttle.failure(ip, now)
		return "", ErrSetupTokenInvalid
	}
	name, err := validateUsername(username)
	if err != nil {
		return "", err
	}
	if err := validatePassword(password); err != nil {
		return "", err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	account := store.AdminAccount{Username: name, PasswordHash: hash, CreatedAt: now, UpdatedAt: now}
	created, err := s.repo.CreateAdmin(ctx, account)
	if err != nil {
		return "", fmt.Errorf("create admin: %w", err)
	}
	if !created {
		return "", ErrAlreadySetUp
	}
	s.admin = &account
	s.setupToken = ""
	s.throttle.success(ip)
	return s.newSession(ctx, now)
}

// Login checks the admin password and, when two-factor is enabled, the code
// (a TOTP code or an unused recovery code). It returns a new session token.
func (s *Service) Login(ctx context.Context, ip, username, password, code string) (string, error) {
	now := s.now()
	if err := s.throttle.check(ip, now); err != nil {
		return "", err
	}

	// Argon2 runs outside the lock against a copy of the hash, so a concurrent
	// ChangePassword cannot race with the read.
	s.mu.RLock()
	var hash string
	var storedName string
	if s.admin != nil {
		hash, storedName = s.admin.PasswordHash, s.admin.Username
	}
	s.mu.RUnlock()
	if hash == "" {
		hash = s.dummyHash
	}
	passwordOK := verifyPassword(hash, password)
	nameOK := storedName != "" && subtle.ConstantTimeCompare([]byte(strings.TrimSpace(username)), []byte(storedName)) == 1
	if !passwordOK || !nameOK {
		s.throttle.failure(ip, now)
		return "", ErrInvalidCredentials
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.admin == nil {
		return "", ErrInvalidCredentials
	}
	if s.admin.TOTPEnabled {
		if code == "" {
			return "", ErrTOTPRequired
		}
		ok, err := s.checkSecondFactor(ctx, code, now)
		if err != nil {
			return "", err
		}
		if !ok {
			s.throttle.failure(ip, now)
			return "", ErrInvalidCredentials
		}
	}
	s.throttle.success(ip)
	return s.newSession(ctx, now)
}

// Session resolves a session token. It fails closed on any database error.
// Last-seen is written at most once per sessionTouchInterval.
func (s *Service) Session(ctx context.Context, sessionToken string) (SessionInfo, bool) {
	hash := sha256Hex(sessionToken)
	sess, found, err := s.repo.Session(ctx, hash)
	if err != nil || !found {
		return SessionInfo{}, false
	}
	now := s.now()
	if !now.Before(sess.ExpiresAt) || !now.Before(sess.LastSeenAt.Add(s.sessionIdle)) {
		// Lazy cleanup. If the delete fails the session is still rejected here.
		_ = s.repo.DeleteSession(ctx, hash)
		return SessionInfo{}, false
	}
	if now.Sub(sess.LastSeenAt) >= sessionTouchInterval {
		if err := s.repo.TouchSession(ctx, hash, now); err != nil {
			return SessionInfo{}, false
		}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.admin == nil {
		return SessionInfo{}, false
	}
	return SessionInfo{Username: s.admin.Username, TwoFactorEnabled: s.admin.TOTPEnabled}, true
}

// Logout deletes the session behind sessionToken.
func (s *Service) Logout(ctx context.Context, sessionToken string) error {
	if err := s.repo.DeleteSession(ctx, sha256Hex(sessionToken)); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// Status describes the admin account. It does not hit the database.
func (s *Service) Status(ctx context.Context) (Status, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.admin == nil {
		return Status{SetupRequired: true}, nil
	}
	return Status{Username: s.admin.Username, TwoFactorEnabled: s.admin.TOTPEnabled}, nil
}

// ChangePassword replaces the admin password after verifying the current one,
// and revokes every session except sessionToken.
func (s *Service) ChangePassword(ctx context.Context, ip, sessionToken, current, next string) error {
	if err := validatePassword(next); err != nil {
		return err
	}
	now := s.now()
	if err := s.throttle.check(ip, now); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.admin == nil {
		return ErrNotSetUp
	}
	if !verifyPassword(s.admin.PasswordHash, current) {
		s.throttle.failure(ip, now)
		return ErrInvalidCredentials
	}
	hash, err := hashPassword(next)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	if err := s.repo.UpdateAdminPassword(ctx, hash, now); err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	s.admin.PasswordHash = hash
	if err := s.repo.DeleteSessionsExcept(ctx, sha256Hex(sessionToken)); err != nil {
		return fmt.Errorf("revoke other sessions: %w", err)
	}
	s.throttle.success(ip)
	return nil
}

// BeginTwoFactor stores a new pending TOTP secret and returns it with its
// otpauth URI. Two-factor stays disabled until ConfirmTwoFactor succeeds.
func (s *Service) BeginTwoFactor(ctx context.Context) (TwoFactorEnrollment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.admin == nil {
		return TwoFactorEnrollment{}, ErrNotSetUp
	}
	if s.admin.TOTPEnabled {
		return TwoFactorEnrollment{}, ErrTOTPAlreadyEnabled
	}
	secret, err := newTOTPSecret()
	if err != nil {
		return TwoFactorEnrollment{}, err
	}
	if err := s.repo.SetPendingTOTPSecret(ctx, secret, s.now()); err != nil {
		return TwoFactorEnrollment{}, fmt.Errorf("store totp secret: %w", err)
	}
	s.admin.TOTPSecret = secret
	return TwoFactorEnrollment{Secret: secret, URI: totpURI(s.admin.Username, secret)}, nil
}

// ConfirmTwoFactor enables two-factor after a valid TOTP code for the pending
// secret. It returns the recovery codes, shown once.
func (s *Service) ConfirmTwoFactor(ctx context.Context, ip, code string) ([]string, error) {
	now := s.now()
	if err := s.throttle.check(ip, now); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.admin == nil {
		return nil, ErrNotSetUp
	}
	if s.admin.TOTPEnabled {
		return nil, ErrTOTPAlreadyEnabled
	}
	if s.admin.TOTPSecret == "" {
		return nil, ErrTOTPNotEnrolled
	}
	step, ok := totpVerify(s.admin.TOTPSecret, strings.TrimSpace(code), now, s.admin.TOTPLastStep)
	if !ok {
		s.throttle.failure(ip, now)
		return nil, ErrTOTPInvalid
	}
	codes, hashes, err := newRecoveryCodes()
	if err != nil {
		return nil, err
	}
	if err := s.repo.EnableTOTP(ctx, step, hashes, now); err != nil {
		return nil, fmt.Errorf("enable totp: %w", err)
	}
	s.admin.TOTPEnabled = true
	s.admin.TOTPLastStep = step
	s.throttle.success(ip)
	return codes, nil
}

// DisableTwoFactor turns two-factor off. It requires the password and a TOTP
// or recovery code.
func (s *Service) DisableTwoFactor(ctx context.Context, ip, password, code string) error {
	now := s.now()
	if err := s.throttle.check(ip, now); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.admin == nil {
		return ErrNotSetUp
	}
	if !verifyPassword(s.admin.PasswordHash, password) {
		s.throttle.failure(ip, now)
		return ErrInvalidCredentials
	}
	if !s.admin.TOTPEnabled {
		return ErrTOTPNotEnabled
	}
	if code == "" {
		return ErrTOTPRequired
	}
	ok, err := s.checkSecondFactor(ctx, code, now)
	if err != nil {
		return err
	}
	if !ok {
		s.throttle.failure(ip, now)
		return ErrTOTPInvalid
	}
	if err := s.repo.DisableTOTP(ctx, now); err != nil {
		return fmt.Errorf("disable totp: %w", err)
	}
	s.admin.TOTPSecret = ""
	s.admin.TOTPEnabled = false
	s.admin.TOTPLastStep = 0
	s.throttle.success(ip)
	return nil
}

// ResetAdmin deletes the admin account, its recovery codes and all sessions.
// API keys are kept. A new setup token is generated.
func (s *Service) ResetAdmin(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.repo.DeleteAdmin(ctx); err != nil {
		return fmt.Errorf("delete admin: %w", err)
	}
	token, err := randomString(setupTokenBytes)
	if err != nil {
		return err
	}
	s.admin = nil
	s.setupToken = token
	return nil
}

// checkSecondFactor accepts a TOTP code (recording its step) or an unused
// recovery code. The caller must hold s.mu for writing and must have checked
// that two-factor is enabled.
func (s *Service) checkSecondFactor(ctx context.Context, code string, now time.Time) (bool, error) {
	if step, ok := totpVerify(s.admin.TOTPSecret, strings.TrimSpace(code), now, s.admin.TOTPLastStep); ok {
		if err := s.repo.UpdateTOTPLastStep(ctx, step, now); err != nil {
			return false, fmt.Errorf("record totp step: %w", err)
		}
		s.admin.TOTPLastStep = step
		return true, nil
	}
	consumed, err := s.repo.ConsumeRecoveryCode(ctx, hashRecoveryCode(code))
	if err != nil {
		return false, fmt.Errorf("consume recovery code: %w", err)
	}
	return consumed, nil
}

// newSession creates a session and returns its plaintext token. Only the hash is stored.
func (s *Service) newSession(ctx context.Context, now time.Time) (string, error) {
	token, err := randomString(sessionTokenBytes)
	if err != nil {
		return "", err
	}
	err = s.repo.CreateSession(ctx, store.AdminSession{
		TokenHash:  sha256Hex(token),
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(s.sessionAbsolute),
	})
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	return token, nil
}

func validateUsername(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxUsernameLength || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", ErrInvalidUsername
	}
	return name, nil
}

func validatePassword(password string) error {
	switch {
	case len(password) < MinPasswordLength:
		return ErrPasswordTooShort
	case len(password) > maxPasswordLength:
		return ErrPasswordTooLong
	}
	return nil
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("read random bytes: %w", err)
	}
	return b, nil
}

func randomString(n int) (string, error) {
	b, err := randomBytes(n)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
