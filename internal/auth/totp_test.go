package auth

import (
	"encoding/base32"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RFC 6238 Appendix B SHA1 vectors, 6 digits, secret "12345678901234567890".
func TestTOTP_RFC6238Vectors(t *testing.T) {
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	tests := []struct {
		unix int64
		want string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
		{20000000000, "353130"},
	}
	for _, tt := range tests {
		t.Run(time.Unix(tt.unix, 0).UTC().Format(time.RFC3339), func(t *testing.T) {
			step, ok := totpVerify(secret, tt.want, time.Unix(tt.unix, 0), 0)
			assert.True(t, ok)
			assert.Equal(t, tt.unix/30, step)
		})
	}
}

func TestTOTP_RejectsReplayAndOutOfWindow(t *testing.T) {
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	now := time.Unix(59, 0)
	tests := []struct {
		name     string
		code     string
		at       time.Time
		lastStep int64
		wantOK   bool
	}{
		{"current step accepted", "287082", now, 0, true},
		{"same step after use rejected", "287082", now, 1, false},
		{"previous step accepted", "287082", now.Add(30 * time.Second), 0, true},
		{"two steps away rejected", "287082", now.Add(60 * time.Second), 0, false},
		{"malformed length rejected", "28708", now, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := totpVerify(secret, tt.code, tt.at, tt.lastStep)
			assert.Equal(t, tt.wantOK, ok)
		})
	}
}

func TestTOTP_SecretFormat(t *testing.T) {
	secret, err := newTOTPSecret()
	require.NoError(t, err)
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	require.NoError(t, err)
	assert.Len(t, raw, 20)
	assert.Equal(t, "otpauth://totp/llama-swap:admin?secret="+secret+"&issuer=llama-swap&algorithm=SHA1&digits=6&period=30",
		totpURI("admin", secret))
}
