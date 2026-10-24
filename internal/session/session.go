// Package session stores the sign-in sessions of members and makes the CSRF
// tokens of the forms.
//
// A session token is 32 random bytes in base64url. The browser holds the token.
// The database holds only its SHA-256, so a copy of the table cannot sign
// anyone in.
package session

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/evanwtf/gobb/internal/db"
)

const (
	// RememberFor is the lifetime of a "remember me" session.
	RememberFor = 365 * 24 * time.Hour
	// DefaultFor is the lifetime of any other session.
	DefaultFor = 24 * time.Hour
	// touchAfter is how stale last_seen_at must be before Touch writes.
	touchAfter = 5 * time.Minute
	// maxUserAgent is the longest user agent that Create stores, in bytes.
	maxUserAgent = 512
)

// ErrNone means that no live session has the token: the token is unknown, was
// deleted, or has expired.
var ErrNone = errors.New("session: no such session")

// Session is one row of the sessions table.
type Session struct {
	// ID is the hex SHA-256 of the token.
	ID         string
	UserID     int64
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	Remember   bool
}

// lifetime returns how long a session lives after its last touch.
func lifetime(remember bool) time.Duration {
	if remember {
		return RememberFor
	}
	return DefaultFor
}

// hashToken returns the ID of the session that has token.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// cutUserAgent makes ua fit the column: valid UTF-8, no NUL, and at most 512
// bytes, cut between characters.
func cutUserAgent(ua string) string {
	ua = strings.ToValidUTF8(ua, "")
	ua = strings.ReplaceAll(ua, "\x00", "")
	if len(ua) <= maxUserAgent {
		return ua
	}
	cut := maxUserAgent
	for cut > 0 && !utf8.RuneStart(ua[cut]) {
		cut--
	}
	return ua[:cut]
}

// Create starts a session for userID and returns the token for the cookie. It
// stores the address and the user agent (cut to 512 bytes), and records the
// address in user_ips. The remember flag picks the lifetime.
func Create(ctx context.Context, q db.Querier, userID int64, remember bool, ip netip.Addr, userAgent string, now time.Time) (string, Session, error) {
	if !ip.IsValid() {
		return "", Session{}, errors.New("session: create: no client address")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", Session{}, fmt.Errorf("session: create: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	s := Session{
		ID: hashToken(token), UserID: userID, CreatedAt: now, LastSeenAt: now,
		ExpiresAt: now.Add(lifetime(remember)), Remember: remember,
	}
	// One statement, so that the session and the address stand or fall together.
	_, err := q.Exec(ctx, `
with s as (
	insert into sessions (id, user_id, created_at, last_seen_at, expires_at, ip, user_agent, remember)
	values ($1, $2, $3, $3, $4, $5, $6, $7)
)
insert into user_ips (user_id, ip, last_seen_at) values ($2, $5, $3)
on conflict (user_id, ip) do update set last_seen_at = excluded.last_seen_at`,
		s.ID, userID, now, s.ExpiresAt, ip.Unmap().WithZone(""), cutUserAgent(userAgent), remember)
	if err != nil {
		return "", Session{}, fmt.Errorf("session: create for user %d: %w", userID, err)
	}
	return token, s, nil
}

// Lookup returns the live session that has token. A session is live while now
// is before its expiry and its user is active. An unknown, deleted or expired
// token, and the token of a user who is not active, give [ErrNone]. The rule is
// here, in one query, so that no caller has to remember it.
func Lookup(ctx context.Context, q db.Querier, token string, now time.Time) (Session, error) {
	if token == "" || len(token) > 128 {
		return Session{}, ErrNone
	}
	var s Session
	err := q.QueryRow(ctx, `
select s.id, s.user_id, s.created_at, s.last_seen_at, s.expires_at, s.remember
  from sessions s join users u on u.id = s.user_id
 where s.id = $1 and s.expires_at > $2 and u.state = 'active'`, hashToken(token), now).
		Scan(&s.ID, &s.UserID, &s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt, &s.Remember)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNone
	}
	if err != nil {
		return Session{}, fmt.Errorf("session: lookup: %w", err)
	}
	return s, nil
}

// NeedsTouch reports whether [Touch] would write at now: the session was last
// seen more than five minutes ago.
func NeedsTouch(s Session, now time.Time) bool {
	return now.Sub(s.LastSeenAt) > touchAfter
}

// Touch records that the session is in use. When last_seen_at is more than five
// minutes old, it sets last_seen_at to now and moves expires_at to now plus the
// lifetime of the session. Otherwise it does nothing, so a page view costs no
// write.
func Touch(ctx context.Context, q db.Querier, s Session, now time.Time) error {
	if !NeedsTouch(s, now) {
		return nil
	}
	_, err := q.Exec(ctx, `update sessions set last_seen_at = $2, expires_at = $3 where id = $1`,
		s.ID, now, now.Add(lifetime(s.Remember)))
	if err != nil {
		return fmt.Errorf("session: touch: %w", err)
	}
	return nil
}

// Delete ends the session that has token. A token that matches no session is
// not an error.
func Delete(ctx context.Context, q db.Querier, token string) error {
	if _, err := q.Exec(ctx, `delete from sessions where id = $1`, hashToken(token)); err != nil {
		return fmt.Errorf("session: delete: %w", err)
	}
	return nil
}

// csrfLabel is the text that the CSRF HMAC covers. A change of version here
// makes every old token fail.
const csrfLabel = "gobb csrf v1"

// CSRFToken returns the token for the forms of a visitor whose secret is secret.
// The secret is the session token of a member, or the gobb_csrf cookie of a
// guest. The token is base64url of HMAC-SHA256 keyed with the secret.
func CSRFToken(secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(csrfLabel))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// CheckCSRF reports whether got is the token for secret. It compares in
// constant time. An empty secret never validates.
func CheckCSRF(secret, got string) bool {
	if secret == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(CSRFToken(secret)), []byte(got)) == 1
}
