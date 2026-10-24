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
	"github.com/jackc/pgx/v5/pgxpool"

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

// ErrStale means that [CreateChecked] did not start a session: the user row no
// longer holds the hash that the sign-in verified, the user is not active, or a
// user ban holds. The sign-in answers it like a wrong password.
var ErrStale = errors.New("session: the account changed during the sign-in")

// Create starts a session for userID and returns the token for the cookie. It
// stores the address and the user agent (cut to 512 bytes), and records the
// address in user_ips. The remember flag picks the lifetime.
//
// Create checks nothing about the user. A sign-in uses [CreateChecked].
func Create(ctx context.Context, q db.Querier, userID int64, remember bool, ip netip.Addr, userAgent string, now time.Time) (string, Session, error) {
	return create(ctx, q, userID, remember, ip, userAgent, now)
}

// CreateChecked is [Create] for a sign-in. It starts the session only while the
// user row still holds wantHash (the hash that the sign-in verified), the user is
// active, and no user ban that has not ended holds the user; otherwise it returns
// [ErrStale] and writes nothing. The check and the insert run in one transaction,
// and the check reads the user row with FOR SHARE.
//
// That lock orders the sign-in against every write that ends the sessions of a
// member: such a write takes the row FOR UPDATE first (a password change, and
// the ban and the reset of an administrator). If the sign-in holds the row, the
// write waits, and then deletes the new session with the others. If the write
// holds it, the sign-in waits, then sees the new hash or the ban, and is refused.
// A sign-in that proved a password cannot outlive a change of it.
func CreateChecked(ctx context.Context, pool *pgxpool.Pool, userID int64, wantHash string, remember bool, ip netip.Addr, userAgent string, now time.Time) (string, Session, error) {
	var (
		token string
		sess  Session
	)
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var hash string
		err := tx.QueryRow(ctx, `select password_hash from users where id = $1 and state = 'active' for share`, userID).Scan(&hash)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrStale
		}
		if err != nil {
			return fmt.Errorf("session: check user %d: %w", userID, err)
		}
		if wantHash == "" || subtle.ConstantTimeCompare([]byte(hash), []byte(wantHash)) != 1 {
			return ErrStale
		}
		var banned bool
		err = tx.QueryRow(ctx, `select exists (select 1 from bans where kind = 'user' and user_id = $1 and (ends_at is null or ends_at > $2))`,
			userID, now).Scan(&banned)
		if err != nil {
			return fmt.Errorf("session: check bans of user %d: %w", userID, err)
		}
		if banned {
			return ErrStale
		}
		token, sess, err = create(ctx, tx, userID, remember, ip, userAgent, now)
		return err
	})
	if err != nil {
		return "", Session{}, err
	}
	return token, sess, nil
}

func create(ctx context.Context, q db.Querier, userID int64, remember bool, ip netip.Addr, userAgent string, now time.Time) (string, Session, error) {
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
	// The unspecified address (0.0.0.0 or ::) stands for every client whose address
	// could not be read, so it is never stored in user_ips.
	ip = ip.Unmap().WithZone("")
	_, err := q.Exec(ctx, `
with s as (
	insert into sessions (id, user_id, created_at, last_seen_at, expires_at, ip, user_agent, remember)
	values ($1, $2, $3, $3, $4, $5, $6, $7)
)
insert into user_ips (user_id, ip, last_seen_at)
select $2::bigint, $5::inet, $3::timestamptz where $8::bool
on conflict (user_id, ip) do update set last_seen_at = excluded.last_seen_at`,
		s.ID, userID, now, s.ExpiresAt, ip, cutUserAgent(userAgent), remember, !ip.IsUnspecified())
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

// DeleteUser ends every session of the member userID and returns how many it
// deleted. A password change calls it, and so does a password reset. A member
// with no session is not an error.
func DeleteUser(ctx context.Context, q db.Querier, userID int64) (int64, error) {
	tag, err := q.Exec(ctx, `delete from sessions where user_id = $1`, userID)
	if err != nil {
		return 0, fmt.Errorf("session: delete sessions of user %d: %w", userID, err)
	}
	return tag.RowsAffected(), nil
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

// ipKeepFor is how long user_ips keeps an address after its last sighting
// (spec 10).
const ipKeepFor = 90 * 24 * time.Hour

// Cleanup deletes the sessions that have expired by now, and the user_ips rows
// last seen more than 90 days before now. It returns how many of each. A session
// that has expired is dead to [Lookup] already, so the delete only frees space.
func Cleanup(ctx context.Context, q db.Querier, now time.Time) (sessions, ips int64, err error) {
	tag, err := q.Exec(ctx, `delete from sessions where expires_at <= $1`, now)
	if err != nil {
		return 0, 0, fmt.Errorf("session: cleanup sessions: %w", err)
	}
	sessions = tag.RowsAffected()
	tag, err = q.Exec(ctx, `delete from user_ips where last_seen_at < $1`, now.Add(-ipKeepFor))
	if err != nil {
		return sessions, 0, fmt.Errorf("session: cleanup user_ips: %w", err)
	}
	return sessions, tag.RowsAffected(), nil
}
