package auth

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"time"
)

const (
	totpPeriod = 30
	totpDigits = 6
	totpSkew   = 1 // accept steps t-1..t+1
)

var totpEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// newTOTPSecret returns 20 random bytes as unpadded uppercase base32.
func newTOTPSecret() (string, error) {
	b, err := randomBytes(20)
	if err != nil {
		return "", err
	}
	return totpEncoding.EncodeToString(b), nil
}

// totpURI returns the otpauth URI an authenticator app imports.
func totpURI(username, secret string) string {
	return fmt.Sprintf("otpauth://totp/llama-swap:%s?secret=%s&issuer=llama-swap&algorithm=SHA1&digits=6&period=30",
		url.PathEscape(username), secret)
}

// totpVerify checks code against the base32 secret at time now. Only steps
// after lastStep are accepted. It returns the matching step so the caller can
// record it.
func totpVerify(secret, code string, now time.Time, lastStep int64) (int64, bool) {
	key, err := totpEncoding.DecodeString(secret)
	if err != nil || len(code) != totpDigits {
		return 0, false
	}
	current := now.Unix() / totpPeriod
	matched := int64(0)
	ok := false
	for step := current - totpSkew; step <= current+totpSkew; step++ {
		if step <= lastStep {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(code), []byte(totpCode(key, step))) == 1 {
			matched, ok = step, true
		}
	}
	return matched, ok
}

// totpCode computes the RFC 4226 HOTP value for a time step (RFC 6238 section 4).
func totpCode(key []byte, step int64) string {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", bin%1000000)
}
