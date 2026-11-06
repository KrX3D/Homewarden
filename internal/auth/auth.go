// Package auth holds passwords, session tokens, TOTP, the setup token, login rate
// limiting and the permission catalog.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

// Permission names. "*" grants everything.
const (
	PermAll                = "*"
	PermDevicesView        = "devices.view"
	PermDevicesManage      = "devices.manage"
	PermUpdatesRun         = "updates.run"
	PermBackupsRun         = "backups.run"
	PermBackupsManage      = "backups.manage"
	PermJobsView           = "jobs.view"
	PermCredentialsManage  = "credentials.manage"
	PermIntegrationsManage = "integrations.manage"
	PermSettingsManage     = "settings.manage"
	PermUsersManage        = "users.manage"
)

// Permissions is the catalog shown when editing a role, in display order.
var Permissions = []string{
	PermDevicesView, PermDevicesManage, PermUpdatesRun, PermBackupsRun, PermBackupsManage,
	PermJobsView, PermCredentialsManage, PermIntegrationsManage, PermSettingsManage, PermUsersManage,
}

// Has reports whether a permission list grants perm.
func Has(perms []string, perm string) bool {
	return slices.Contains(perms, PermAll) || slices.Contains(perms, perm)
}

// ValidPermission reports whether p is a known permission.
func ValidPermission(p string) bool { return p == PermAll || slices.Contains(Permissions, p) }

// ---- passwords

// MinPasswordLength is enforced for every password.
const MinPasswordLength = 10

// ErrWeakPassword means the password is too short.
var ErrWeakPassword = errors.New("password too short")

// HashPassword returns a bcrypt hash.
func HashPassword(pw string) (string, error) {
	if utf8.RuneCountInString(pw) < MinPasswordLength {
		return "", ErrWeakPassword
	}
	b, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
	return string(b), err
}

// CheckPassword compares a password with its hash.
func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// dummyHash is compared against when a username does not exist, so a login for an
// unknown user takes as long as one for a known user.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("homewarden-timing-equaliser"), 12)

// BurnTime spends the same time as a failed password check.
func BurnTime(pw string) { bcrypt.CompareHashAndPassword(dummyHash, []byte(pw)) }

// ---- tokens

// NewToken returns a random URL-safe token and the hash stored in the database.
func NewToken() (token, hash string) {
	b := make([]byte, 32)
	rand.Read(b)
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token)
}

// HashToken hashes a session token for storage.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// NewSetupToken returns a short token that is easy to copy from a log line.
func NewSetupToken() string {
	b := make([]byte, 10)
	rand.Read(b)
	s := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
	return s[:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:16]
}

// EqualToken compares two tokens in constant time.
func EqualToken(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ---- rate limiting

// Limiter slows down repeated failed logins per key (username or address).
type Limiter struct {
	mu    sync.Mutex
	fails map[string]*failState
}

type failState struct {
	count int
	until time.Time
}

// NewLimiter returns an empty limiter.
func NewLimiter() *Limiter { return &Limiter{fails: map[string]*failState{}} }

// Blocked returns how long key must still wait.
func (l *Limiter) Blocked(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, ok := l.fails[key]
	if !ok {
		return 0
	}
	if d := time.Until(f.until); d > 0 {
		return d
	}
	return 0
}

// Fail records a failed attempt. After five failures each further one doubles the
// wait, capped at 15 minutes.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, ok := l.fails[key]
	if !ok {
		f = &failState{}
		l.fails[key] = f
	}
	f.count++
	if f.count >= 5 {
		wait := time.Second << min(f.count-5, 10)
		if wait > 15*time.Minute {
			wait = 15 * time.Minute
		}
		f.until = time.Now().Add(wait)
	}
}

// Success clears a key.
func (l *Limiter) Success(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
}
