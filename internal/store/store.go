// Package store defines the data access contracts for llama-swap's persisted
// data. It holds the domain types and repository interfaces only; concrete
// backends live in subpackages such as store/sqlite. Callers depend on the
// interfaces here so a backend can be swapped or faked without touching them.
package store

import (
	"context"
	"time"
)

// Store is the root of data access. It groups one repository per kind of
// persisted data, all backed by the same underlying database, and owns that
// database's lifecycle.
type Store interface {
	// Activity returns the repository for request activity log rows.
	Activity() ActivityRepository

	// Cache returns the repository for cached key/blob records.
	Cache() CacheRepository

	// Auth returns the repository for admin account, session and API key
	// records.
	Auth() AuthRepository

	// IsInMemory reports whether the backing database is not persisted to
	// disk. In-memory stores are bounded by pruning rather than by disk.
	IsInMemory() bool

	// Close releases the backing database. It is safe to call more than once.
	Close() error
}

// ActivityRepository persists and queries activity log entries.
type ActivityRepository interface {
	// Insert stores an entry and returns it with its assigned ID. A zero
	// Timestamp is replaced with the current time.
	Insert(ctx context.Context, entry ActivityLogEntry) (ActivityLogEntry, error)

	// List returns one page of entries matching the query, newest first
	// unless the query says otherwise. Page and Limit are normalized with
	// ActivityQuery.WithDefaults before use.
	List(ctx context.Context, query ActivityQuery) (ActivityPage, error)

	// Stats aggregates totals and speed histograms over the matching entries.
	Stats(ctx context.Context, query ActivityStatsQuery) (ActivityStats, error)

	// Prune deletes all but the newest maxRows entries. A maxRows of zero or
	// less is a no-op.
	Prune(ctx context.Context, maxRows int) error
}

// AdminAccount is the single admin login. TOTPSecret is base32 and stored
// unencrypted because it must be readable to verify codes.
type AdminAccount struct {
	Username     string
	PasswordHash string // argon2id PHC string
	TOTPSecret   string // base32, empty until two-factor enrollment begins
	TOTPEnabled  bool
	// TOTPLastStep is the last accepted TOTP time step. Codes for steps at or
	// below it are rejected so a code can be used only once.
	TOTPLastStep int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// AdminSession is a browser session. TokenHash is the sha256 hex of the
// cookie token; the plaintext token is never stored.
type AdminSession struct {
	TokenHash  string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time // absolute expiry
}

// APIKey is a scoped inference key. KeyHash is the sha256 hex of the
// plaintext key; Prefix is a short display form of the plaintext.
type APIKey struct {
	ID         string
	Name       string
	KeyHash    string
	Prefix     string
	Models     []string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// AuthRepository persists admin account, two-factor recovery, session and API
// key state. Hashes and secrets are passed in already derived; the repository
// never sees plaintext passwords, tokens or keys.
type AuthRepository interface {
	// Admin returns the admin account. found is false when no admin exists.
	Admin(ctx context.Context) (account AdminAccount, found bool, err error)

	// CreateAdmin inserts the admin account. created is false, and nothing is
	// written, when an admin already exists.
	CreateAdmin(ctx context.Context, account AdminAccount) (created bool, err error)

	// UpdateAdminPassword replaces the admin password hash.
	UpdateAdminPassword(ctx context.Context, passwordHash string, at time.Time) error

	// SetPendingTOTPSecret stores a new TOTP secret with two-factor disabled.
	// Any previously stored, not yet enabled, secret is replaced.
	SetPendingTOTPSecret(ctx context.Context, secret string, at time.Time) error

	// EnableTOTP enables two-factor, records lastStep as the last accepted
	// step, and replaces all recovery codes with codeHashes.
	EnableTOTP(ctx context.Context, lastStep int64, codeHashes []string, at time.Time) error

	// UpdateTOTPLastStep records the last accepted TOTP time step.
	UpdateTOTPLastStep(ctx context.Context, step int64, at time.Time) error

	// DisableTOTP clears the TOTP secret, enabled flag and last step, and
	// deletes every recovery code.
	DisableTOTP(ctx context.Context, at time.Time) error

	// ConsumeRecoveryCode deletes the recovery code with codeHash. It reports
	// whether a code was deleted, so a code can be used only once.
	ConsumeRecoveryCode(ctx context.Context, codeHash string) (consumed bool, err error)

	// DeleteAdmin deletes the admin account, all recovery codes and all
	// sessions. API keys are kept.
	DeleteAdmin(ctx context.Context) error

	// CreateSession stores a session.
	CreateSession(ctx context.Context, session AdminSession) error

	// Session returns the session with tokenHash. found is false when none
	// exists.
	Session(ctx context.Context, tokenHash string) (session AdminSession, found bool, err error)

	// TouchSession sets the session's last seen time.
	TouchSession(ctx context.Context, tokenHash string, at time.Time) error

	// DeleteSession deletes the session with tokenHash. Deleting a missing
	// session is not an error.
	DeleteSession(ctx context.Context, tokenHash string) error

	// DeleteSessionsExcept deletes every session whose token hash differs
	// from keepTokenHash.
	DeleteSessionsExcept(ctx context.Context, keepTokenHash string) error

	// PruneSessions deletes sessions past their absolute expiry or idle for
	// longer than idle, measured at now.
	PruneSessions(ctx context.Context, now time.Time, idle time.Duration) error

	// CreateAPIKey stores an API key.
	CreateAPIKey(ctx context.Context, key APIKey) error

	// APIKeys returns every API key, including revoked ones, oldest first.
	APIKeys(ctx context.Context) ([]APIKey, error)

	// RevokeAPIKey marks the key with id as revoked at at. revoked is false
	// when no active key has that id.
	RevokeAPIKey(ctx context.Context, id string, at time.Time) (revoked bool, err error)

	// TouchAPIKey sets the key's last used time.
	TouchAPIKey(ctx context.Context, id string, at time.Time) error
}
