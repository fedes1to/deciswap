-- +goose Up
CREATE TABLE admin_account (
    id              INTEGER PRIMARY KEY CHECK (id = 1),
    username        TEXT    NOT NULL,
    password_hash   TEXT    NOT NULL,            -- argon2id PHC string
    totp_secret     TEXT    NOT NULL DEFAULT '', -- base32, '' when never enrolled
    totp_enabled    INTEGER NOT NULL DEFAULT 0,
    totp_last_step  INTEGER NOT NULL DEFAULT 0,  -- replay guard
    created_at      INTEGER NOT NULL,            -- unix seconds
    updated_at      INTEGER NOT NULL
);

CREATE TABLE admin_recovery_codes (
    code_hash  TEXT PRIMARY KEY,                 -- sha256 hex of normalized code
    created_at INTEGER NOT NULL
);

CREATE TABLE admin_sessions (
    token_hash   TEXT PRIMARY KEY,               -- sha256 hex of cookie token
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL                -- absolute expiry
);

CREATE TABLE api_keys (
    id           TEXT PRIMARY KEY,               -- random hex, 16 bytes
    name         TEXT    NOT NULL,
    key_hash     TEXT    NOT NULL UNIQUE,        -- sha256 hex of plaintext key
    prefix       TEXT    NOT NULL,               -- display prefix, e.g. "sk-ls-Ab3dE"
    models       TEXT    NOT NULL,               -- JSON array of strings
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER,
    revoked_at   INTEGER
);

-- +goose Down
DROP TABLE api_keys;
DROP TABLE admin_sessions;
DROP TABLE admin_recovery_codes;
DROP TABLE admin_account;
