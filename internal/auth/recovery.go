package auth

import (
	"crypto/rand"
	"fmt"
	"strings"
)

const (
	recoveryCodeCount = 8
	recoveryHalfLen   = 5
	// recoveryAlphabet is base32 without 0, 1, 8, O, I and L, so codes read
	// unambiguously. It has 29 symbols. Bytes at or above recoveryDrawLimit
	// (232, the largest multiple of 29 below 256) are redrawn to keep the
	// selection uniform.
	recoveryAlphabet  = "ABCDEFGHJKMNPQRSTUVWXYZ234567"
	recoveryDrawLimit = 232
)

// newRecoveryCodes returns recoveryCodeCount codes formatted XXXXX-XXXXX, and
// their hashes for storage. Plaintext codes are shown to the admin once.
func newRecoveryCodes() ([]string, []string, error) {
	codes := make([]string, 0, recoveryCodeCount)
	hashes := make([]string, 0, recoveryCodeCount)
	for range recoveryCodeCount {
		raw, err := randomRecoveryChars(2 * recoveryHalfLen)
		if err != nil {
			return nil, nil, err
		}
		code := raw[:recoveryHalfLen] + "-" + raw[recoveryHalfLen:]
		codes = append(codes, code)
		hashes = append(hashes, hashRecoveryCode(code))
	}
	return codes, hashes, nil
}

// hashRecoveryCode normalises user input (case, dashes, spaces) and hashes it.
func hashRecoveryCode(code string) string {
	normalised := strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(code))
	return sha256Hex(normalised)
}

func randomRecoveryChars(n int) (string, error) {
	out := make([]byte, 0, n)
	buf := make([]byte, 1)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("read random bytes: %w", err)
		}
		if int(buf[0]) >= recoveryDrawLimit {
			continue
		}
		out = append(out, recoveryAlphabet[int(buf[0])%len(recoveryAlphabet)])
	}
	return string(out), nil
}
