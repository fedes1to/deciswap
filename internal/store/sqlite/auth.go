package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mostlygeek/llama-swap/internal/store"
)

// authRepository implements store.AuthRepository over the admin_account,
// admin_recovery_codes, admin_sessions and api_keys tables. Times are stored
// as unix seconds.
type authRepository struct {
	db *sql.DB
}

var _ store.AuthRepository = (*authRepository)(nil)

// adminAccountID is the only id the admin_account table accepts.
const adminAccountID = 1

func (r *authRepository) Admin(ctx context.Context) (store.AdminAccount, bool, error) {
	var (
		a                    store.AdminAccount
		totpEnabled          int
		createdAt, updatedAt int64
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT username, password_hash, totp_secret, totp_enabled, totp_last_step,
		       created_at, updated_at
		FROM admin_account WHERE id = ?`,
		adminAccountID,
	).Scan(&a.Username, &a.PasswordHash, &a.TOTPSecret, &totpEnabled, &a.TOTPLastStep,
		&createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return store.AdminAccount{}, false, nil
	}
	if err != nil {
		return store.AdminAccount{}, false, fmt.Errorf("get admin account: %w", err)
	}
	a.TOTPEnabled = totpEnabled != 0
	a.CreatedAt = time.Unix(createdAt, 0)
	a.UpdatedAt = time.Unix(updatedAt, 0)
	return a, true, nil
}

func (r *authRepository) CreateAdmin(ctx context.Context, a store.AdminAccount) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO admin_account (id, username, password_hash, totp_secret, totp_enabled,
		                           totp_last_step, created_at, updated_at)
		SELECT ?, ?, ?, '', 0, 0, ?, ?
		WHERE NOT EXISTS (SELECT 1 FROM admin_account WHERE id = ?)`,
		adminAccountID, a.Username, a.PasswordHash, a.CreatedAt.Unix(), a.UpdatedAt.Unix(),
		adminAccountID,
	)
	if err != nil {
		return false, fmt.Errorf("create admin account: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("create admin account: %w", err)
	}
	return n == 1, nil
}

func (r *authRepository) UpdateAdminPassword(ctx context.Context, passwordHash string, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE admin_account SET password_hash = ?, updated_at = ? WHERE id = ?`,
		passwordHash, at.Unix(), adminAccountID,
	)
	if err != nil {
		return fmt.Errorf("update admin password: %w", err)
	}
	return nil
}

func (r *authRepository) SetPendingTOTPSecret(ctx context.Context, secret string, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE admin_account
		SET totp_secret = ?, totp_enabled = 0, totp_last_step = 0, updated_at = ?
		WHERE id = ?`,
		secret, at.Unix(), adminAccountID,
	)
	if err != nil {
		return fmt.Errorf("set pending totp secret: %w", err)
	}
	return nil
}

func (r *authRepository) EnableTOTP(ctx context.Context, lastStep int64, codeHashes []string, at time.Time) error {
	return r.withTx(ctx, "enable totp", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			UPDATE admin_account
			SET totp_enabled = 1, totp_last_step = ?, updated_at = ?
			WHERE id = ?`,
			lastStep, at.Unix(), adminAccountID,
		); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM admin_recovery_codes`); err != nil {
			return err
		}
		for _, h := range codeHashes {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO admin_recovery_codes (code_hash, created_at) VALUES (?, ?)`,
				h, at.Unix(),
			); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *authRepository) UpdateTOTPLastStep(ctx context.Context, step int64, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE admin_account SET totp_last_step = ?, updated_at = ? WHERE id = ?`,
		step, at.Unix(), adminAccountID,
	)
	if err != nil {
		return fmt.Errorf("update totp last step: %w", err)
	}
	return nil
}

func (r *authRepository) DisableTOTP(ctx context.Context, at time.Time) error {
	return r.withTx(ctx, "disable totp", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			UPDATE admin_account
			SET totp_secret = '', totp_enabled = 0, totp_last_step = 0, updated_at = ?
			WHERE id = ?`,
			at.Unix(), adminAccountID,
		); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM admin_recovery_codes`)
		return err
	})
}

func (r *authRepository) ConsumeRecoveryCode(ctx context.Context, codeHash string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		DELETE FROM admin_recovery_codes WHERE code_hash = ?`,
		codeHash,
	)
	if err != nil {
		return false, fmt.Errorf("consume recovery code: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("consume recovery code: %w", err)
	}
	return n == 1, nil
}

func (r *authRepository) DeleteAdmin(ctx context.Context) error {
	return r.withTx(ctx, "delete admin", func(tx *sql.Tx) error {
		for _, q := range []string{
			`DELETE FROM admin_account`,
			`DELETE FROM admin_recovery_codes`,
			`DELETE FROM admin_sessions`,
		} {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *authRepository) CreateSession(ctx context.Context, s store.AdminSession) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO admin_sessions (token_hash, created_at, last_seen_at, expires_at)
		VALUES (?, ?, ?, ?)`,
		s.TokenHash, s.CreatedAt.Unix(), s.LastSeenAt.Unix(), s.ExpiresAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

func (r *authRepository) Session(ctx context.Context, tokenHash string) (store.AdminSession, bool, error) {
	var s store.AdminSession
	var createdAt, lastSeenAt, expiresAt int64
	err := r.db.QueryRowContext(ctx, `
		SELECT created_at, last_seen_at, expires_at FROM admin_sessions WHERE token_hash = ?`,
		tokenHash,
	).Scan(&createdAt, &lastSeenAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return store.AdminSession{}, false, nil
	}
	if err != nil {
		return store.AdminSession{}, false, fmt.Errorf("get session: %w", err)
	}
	s.TokenHash = tokenHash
	s.CreatedAt = time.Unix(createdAt, 0)
	s.LastSeenAt = time.Unix(lastSeenAt, 0)
	s.ExpiresAt = time.Unix(expiresAt, 0)
	return s, true, nil
}

func (r *authRepository) TouchSession(ctx context.Context, tokenHash string, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE admin_sessions SET last_seen_at = ? WHERE token_hash = ?`,
		at.Unix(), tokenHash,
	)
	if err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	return nil
}

func (r *authRepository) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE token_hash = ?`, tokenHash)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

func (r *authRepository) DeleteSessionsExcept(ctx context.Context, keepTokenHash string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE token_hash <> ?`, keepTokenHash)
	if err != nil {
		return fmt.Errorf("delete other sessions: %w", err)
	}
	return nil
}

func (r *authRepository) PruneSessions(ctx context.Context, now time.Time, idle time.Duration) error {
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM admin_sessions WHERE expires_at <= ? OR last_seen_at + ? <= ?`,
		now.Unix(), int64(idle/time.Second), now.Unix(),
	)
	if err != nil {
		return fmt.Errorf("prune sessions: %w", err)
	}
	return nil
}

func (r *authRepository) CreateAPIKey(ctx context.Context, k store.APIKey) error {
	models, err := json.Marshal(k.Models)
	if err != nil {
		return fmt.Errorf("encode api key models: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO api_keys (id, name, key_hash, prefix, models, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		k.ID, k.Name, k.KeyHash, k.Prefix, string(models), k.CreatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("create api key: %w", err)
	}
	return nil
}

func (r *authRepository) APIKeys(ctx context.Context) ([]store.APIKey, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, key_hash, prefix, models, created_at, last_used_at, revoked_at
		FROM api_keys ORDER BY created_at, rowid`)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer rows.Close()

	var keys []store.APIKey
	for rows.Next() {
		var (
			k                     store.APIKey
			models                string
			createdAt             int64
			lastUsedAt, revokedAt sql.NullInt64
		)
		if err := rows.Scan(&k.ID, &k.Name, &k.KeyHash, &k.Prefix, &models,
			&createdAt, &lastUsedAt, &revokedAt); err != nil {
			return nil, fmt.Errorf("scan api key: %w", err)
		}
		if err := json.Unmarshal([]byte(models), &k.Models); err != nil {
			return nil, fmt.Errorf("decode api key models: %w", err)
		}
		k.CreatedAt = time.Unix(createdAt, 0)
		if lastUsedAt.Valid {
			t := time.Unix(lastUsedAt.Int64, 0)
			k.LastUsedAt = &t
		}
		if revokedAt.Valid {
			t := time.Unix(revokedAt.Int64, 0)
			k.RevokedAt = &t
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	return keys, nil
}

func (r *authRepository) RevokeAPIKey(ctx context.Context, id string, at time.Time) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE api_keys SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		at.Unix(), id,
	)
	if err != nil {
		return false, fmt.Errorf("revoke api key: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("revoke api key: %w", err)
	}
	return n == 1, nil
}

func (r *authRepository) TouchAPIKey(ctx context.Context, id string, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE api_keys SET last_used_at = ? WHERE id = ?`,
		at.Unix(), id,
	)
	if err != nil {
		return fmt.Errorf("touch api key: %w", err)
	}
	return nil
}

// withTx runs fn in a transaction, rolling back when fn fails. op names the
// operation in the returned error.
func (r *authRepository) withTx(ctx context.Context, op string, fn func(tx *sql.Tx) error) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	return nil
}
