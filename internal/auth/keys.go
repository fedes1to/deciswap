package auth

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/mostlygeek/llama-swap/internal/store"
)

const (
	keyNameMaxLength = 64
	keyDisplayLength = 12
	keyIDBytes       = 16
	keyTouchInterval = time.Minute
)

// KeysConfigured reports whether any API key exists, including revoked ones.
// It does not hit the database.
func (s *Service) KeysConfigured() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.keysExist
}

// CreateKey stores a new scoped key. The plaintext is returned once and never stored.
func (s *Service) CreateKey(ctx context.Context, name string, models []string) (string, KeyInfo, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > keyNameMaxLength {
		return "", KeyInfo{}, ErrKeyNameInvalid
	}
	if len(models) == 0 {
		return "", KeyInfo{}, ErrKeyModelsRequired
	}
	random, err := randomBytes(32)
	if err != nil {
		return "", KeyInfo{}, err
	}
	plaintext := KeyPrefix + base64.RawURLEncoding.EncodeToString(random)
	idBytes, err := randomBytes(keyIDBytes)
	if err != nil {
		return "", KeyInfo{}, err
	}
	key := store.APIKey{
		ID:        hex.EncodeToString(idBytes),
		Name:      name,
		KeyHash:   sha256Hex(plaintext),
		Prefix:    plaintext[:keyDisplayLength],
		Models:    append([]string(nil), models...),
		CreatedAt: s.now(),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.repo.CreateAPIKey(ctx, key); err != nil {
		return "", KeyInfo{}, fmt.Errorf("create api key: %w", err)
	}
	if err := s.loadKeys(ctx); err != nil {
		return "", KeyInfo{}, err
	}
	return plaintext, keyInfo(key), nil
}

// ListKeys returns every key, including revoked ones, oldest first.
func (s *Service) ListKeys(ctx context.Context) ([]KeyInfo, error) {
	keys, err := s.repo.APIKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	infos := make([]KeyInfo, 0, len(keys))
	for _, k := range keys {
		infos = append(infos, keyInfo(k))
	}
	return infos, nil
}

// RevokeKey revokes the active key with id. It takes effect for ResolveKey immediately.
func (s *Service) RevokeKey(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	revoked, err := s.repo.RevokeAPIKey(ctx, id, s.now())
	if err != nil {
		return fmt.Errorf("revoke api key: %w", err)
	}
	if !revoked {
		return ErrKeyNotFound
	}
	return s.loadKeys(ctx)
}

// ResolveKey maps a presented plaintext key to its scope. It reads only the
// in-memory cache. last_used is written at most once per minute per key, and
// a failure there is logged without affecting the result.
func (s *Service) ResolveKey(presented string) (KeyInfo, bool) {
	s.mu.RLock()
	info, ok := s.keys[sha256Hex(presented)]
	s.mu.RUnlock()
	if !ok {
		return KeyInfo{}, false
	}

	now := s.now()
	s.touchMu.Lock()
	touch := now.Sub(s.keyTouched[info.ID]) >= keyTouchInterval
	if touch {
		s.keyTouched[info.ID] = now
	}
	s.touchMu.Unlock()
	if touch {
		if err := s.repo.TouchAPIKey(context.Background(), info.ID, now); err != nil {
			log.Printf("auth: touch api key %s: %v", info.ID, err)
		}
	}
	return info, true
}

// loadKeys rebuilds the in-memory key cache from the database. The caller
// must hold s.mu for writing, or be the only goroutine (New).
func (s *Service) loadKeys(ctx context.Context) error {
	keys, err := s.repo.APIKeys(ctx)
	if err != nil {
		return fmt.Errorf("load api keys: %w", err)
	}
	cache := make(map[string]KeyInfo, len(keys))
	for _, k := range keys {
		if k.RevokedAt == nil {
			cache[k.KeyHash] = keyInfo(k)
		}
	}
	s.keys = cache
	s.keysExist = len(keys) > 0
	return nil
}

func keyInfo(k store.APIKey) KeyInfo {
	return KeyInfo{
		ID:         k.ID,
		Name:       k.Name,
		Prefix:     k.Prefix,
		Models:     append([]string(nil), k.Models...),
		CreatedAt:  k.CreatedAt,
		LastUsedAt: k.LastUsedAt,
		RevokedAt:  k.RevokedAt,
	}
}
