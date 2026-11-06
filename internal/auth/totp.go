package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP per RFC 6238: 30-second steps, 6 digits, SHA-1. This is what every
// authenticator app (Aegis, Google Authenticator, 1Password, ...) expects.

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a random base32 secret.
func NewTOTPSecret() string {
	b := make([]byte, 20)
	rand.Read(b)
	return b32.EncodeToString(b)
}

// TOTPURL is the otpauth:// URL an authenticator app reads from a QR code.
func TOTPURL(secret, username string) string {
	label := url.PathEscape("Homewarden:" + username)
	q := url.Values{"secret": {secret}, "issuer": {"Homewarden"}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
	return "otpauth://totp/" + label + "?" + q.Encode()
}

func totpAt(key []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", code%1_000_000)
}

// TOTPCode returns the code for a given time (used by tests).
func TOTPCode(secret string, t time.Time) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(secret))
	if err != nil {
		return "", err
	}
	return totpAt(key, t.Unix()/30), nil
}

// ValidTOTP accepts the current code and one step either side, for clock drift.
func ValidTOTP(secret, code string, t time.Time) bool {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != 6 {
		return false
	}
	key, err := b32.DecodeString(strings.ToUpper(secret))
	if err != nil {
		return false
	}
	step := t.Unix() / 30
	for d := int64(-1); d <= 1; d++ {
		if hmac.Equal([]byte(totpAt(key, step+d)), []byte(code)) {
			return true
		}
	}
	return false
}
