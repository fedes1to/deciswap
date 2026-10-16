package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPassword_HashVerify(t *testing.T) {
	hash, err := hashPassword("correct horse battery")
	require.NoError(t, err)
	assert.Regexp(t, `^\$argon2id\$v=19\$m=19456,t=2,p=1\$[A-Za-z0-9+/]{22}\$[A-Za-z0-9+/]{43}$`, hash)

	tests := []struct {
		name     string
		encoded  string
		password string
		want     bool
	}{
		{"correct password", hash, "correct horse battery", true},
		{"wrong password", hash, "correct horse batterY", false},
		{"malformed encoding", "not-a-hash", "correct horse battery", false},
		{"wrong algorithm", "$argon2i$v=19$m=19456,t=2,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "x", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, verifyPassword(tt.encoded, tt.password))
		})
	}
}

func TestPassword_HashIsSalted(t *testing.T) {
	a, err := hashPassword("same password here")
	require.NoError(t, err)
	b, err := hashPassword("same password here")
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
}

func TestPassword_MinimumLength(t *testing.T) {
	tests := []struct {
		name     string
		password string
		want     error
	}{
		{"11 bytes rejected", "abcdefghijk", ErrPasswordTooShort},
		{"12 bytes accepted", "abcdefghijkl", nil},
		{"1025 bytes rejected", string(make([]byte, 1025)), ErrPasswordTooLong},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.ErrorIs(t, validatePassword(tt.password), tt.want)
		})
	}
}
