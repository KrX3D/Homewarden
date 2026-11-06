package auth

import (
	"encoding/base32"
	"testing"
	"time"
)

// RFC 6238 appendix B test vector for SHA-1 (secret "12345678901234567890").
func TestTOTPVector(t *testing.T) {
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	cases := map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"}
	for ts, want := range cases {
		got, err := TOTPCode(secret, time.Unix(ts, 0))
		if err != nil || got != want {
			t.Fatalf("t=%d: got %s %v, want %s", ts, got, err, want)
		}
	}
}

func TestValidTOTPWindow(t *testing.T) {
	s := NewTOTPSecret()
	now := time.Now()
	prev, _ := TOTPCode(s, now.Add(-30*time.Second))
	if !ValidTOTP(s, prev, now) {
		t.Fatal("previous step should be accepted")
	}
	old, _ := TOTPCode(s, now.Add(-120*time.Second))
	if old != prev && ValidTOTP(s, old, now) {
		t.Fatal("code from 4 steps ago must be rejected")
	}
}

func TestPassword(t *testing.T) {
	if _, err := HashPassword("short"); err != ErrWeakPassword {
		t.Fatal("short password must be rejected")
	}
	h, err := HashPassword("correct horse battery")
	if err != nil || !CheckPassword(h, "correct horse battery") || CheckPassword(h, "wrong password!") {
		t.Fatal("hash/check mismatch")
	}
}

func TestLimiter(t *testing.T) {
	l := NewLimiter()
	for i := 0; i < 4; i++ {
		l.Fail("x")
	}
	if l.Blocked("x") != 0 {
		t.Fatal("four failures must not block")
	}
	l.Fail("x")
	if l.Blocked("x") == 0 {
		t.Fatal("fifth failure must block")
	}
	l.Success("x")
	if l.Blocked("x") != 0 {
		t.Fatal("success must clear")
	}
}
