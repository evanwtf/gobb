// Package login checks a name and a password against the users table.
//
// [Service.Check] is the one place that decides whether a sign-in succeeds. It
// does not create a session; the web layer does that when Check returns a user.
package login

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/evanwtf/gobb/internal/account"
	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/internal/password"
)

var (
	// ErrWrong means the name or the password is wrong. It does not say which.
	ErrWrong = errors.New("login: the name or password is wrong")
	// ErrAmbiguous means the name is an email address that more than one account
	// uses, and the password matches at least one of them.
	ErrAmbiguous = errors.New("login: the email address belongs to more than one account")
	// ErrInactive means the password is right and the account is not active.
	ErrInactive = errors.New("login: the account is not active")
)

// BannedError means the password is right and a ban holds the account, the
// email address or the client address.
type BannedError struct {
	// Reason is the text of the ban that the member may read (reason_shown). It
	// can be empty.
	Reason string
}

// Error implements error.
func (e *BannedError) Error() string {
	if e.Reason == "" {
		return "login: banned"
	}
	return "login: banned: " + e.Reason
}

// maxName is the longest name that Check looks up, in bytes. No username or
// email address is longer. The limit keeps the cleaning cheap.
const maxName = 512

// DefaultFloor is the least time that a failed check takes.
const DefaultFloor = 400 * time.Millisecond

// Service checks sign-ins.
type Service struct {
	q db.Querier
	// Floor is the least time that a failed [Service.Check] (ErrWrong or
	// ErrAmbiguous) takes, counted from its start. The hashes of the imported
	// board differ a lot in cost, and an unknown name costs one fast check, so
	// without a floor the time to answer would tell a stranger whether a name or
	// an address has an account. New sets it to [DefaultFloor]; a test may set it
	// small. Zero means no floor.
	Floor time.Duration
}

// New returns a Service that reads and writes through q.
func New(q db.Querier) *Service {
	return &Service{q: q, Floor: DefaultFloor}
}

// candidate is a user row that a name can match.
type candidate struct {
	id          int64
	hash        string
	scheme      string
	state       string
	emailClean  string
	matched     bool
	needsRehash bool
}

// Check finds the user for name (a username or an email address) and verifies
// pw. On success it returns the ID of the user.
//
// The order is fixed, so that the errors tell a stranger nothing:
//  1. A name that is not valid UTF-8, or is too long, is [ErrWrong].
//  2. The user whose username_clean equals the cleaned name; if none, the users
//     whose email_clean equals the cleaned name.
//  3. No user is [ErrWrong].
//  4. The password is verified against each user found. If the name is an email
//     address shared by several users, a match on any of them is [ErrAmbiguous]
//     and no match is [ErrWrong]. A login by email never picks one account.
//  5. A wrong password is [ErrWrong]. An empty stored hash never matches.
//  6. Only after a match: an inactive account is [ErrInactive].
//  7. A ban that has not ended is a [*BannedError].
//  8. A hash that needs a rehash is replaced with a gobb hash.
//
// A failure that a stranger can cause with a guess (ErrWrong or ErrAmbiguous)
// returns no sooner than Floor after the call began. The wait ends early when
// ctx ends; the answer does not change.
func (s *Service) Check(ctx context.Context, name, pw string, ip netip.Addr, now time.Time) (int64, error) {
	start := time.Now()
	id, err := s.check(ctx, name, pw, ip, now)
	if errors.Is(err, ErrWrong) || errors.Is(err, ErrAmbiguous) {
		s.wait(ctx, start)
	}
	return id, err
}

// wait sleeps until Floor has passed since start, or until ctx ends.
func (s *Service) wait(ctx context.Context, start time.Time) {
	left := s.Floor - time.Since(start)
	if left <= 0 {
		return
	}
	timer := time.NewTimer(left)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

func (s *Service) check(ctx context.Context, name, pw string, ip netip.Addr, now time.Time) (int64, error) {
	if !utf8.ValidString(name) || len(name) > maxName {
		burn(pw)
		return 0, ErrWrong
	}
	users, err := s.find(ctx, name)
	if err != nil {
		return 0, err
	}
	if len(users) == 0 {
		burn(pw)
		return 0, ErrWrong
	}

	matches := 0
	for i := range users {
		u := &users[i]
		if u.hash == "" {
			burn(pw)
			continue
		}
		u.matched, u.needsRehash = password.Verify(pw, u.hash, u.scheme)
		if u.matched {
			matches++
		}
	}
	switch {
	case matches == 0:
		return 0, ErrWrong
	case len(users) > 1:
		return 0, ErrAmbiguous
	}

	u := users[0]
	if u.state != "active" {
		return 0, ErrInactive
	}
	reason, banned, err := s.banned(ctx, u, ip, now)
	if err != nil {
		return 0, err
	}
	if banned {
		return 0, &BannedError{Reason: reason}
	}
	if u.needsRehash {
		s.rehash(ctx, u, pw)
	}
	return u.id, nil
}

// find returns the users that name can mean: the one with that username, or if
// none, the ones with that email address.
func (s *Service) find(ctx context.Context, name string) ([]candidate, error) {
	const cols = `select id, password_hash, password_scheme, state, email_clean from users where `
	users, err := s.query(ctx, cols+`username_clean = $1 order by id`, account.CleanUsername(name))
	if err != nil || len(users) > 0 {
		return users, err
	}
	email := account.CleanEmail(name)
	if email == "" {
		return nil, nil
	}
	return s.query(ctx, cols+`email_clean = $1 order by id`, email)
}

func (s *Service) query(ctx context.Context, sql string, arg string) ([]candidate, error) {
	rows, err := s.q.Query(ctx, sql, arg)
	if err != nil {
		return nil, fmt.Errorf("login: find user: %w", err)
	}
	defer rows.Close()
	var out []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.hash, &c.scheme, &c.state, &c.emailClean); err != nil {
			return nil, fmt.Errorf("login: find user: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("login: find user: %w", err)
	}
	return out, nil
}

// banned reports whether a ban that has not ended holds u, and returns the text
// of the first such ban that the member may read. The bans are a user ban on
// the ID, an email ban on the address of the user, and an ip ban whose range
// holds the client address.
func (s *Service) banned(ctx context.Context, u candidate, ip netip.Addr, now time.Time) (string, bool, error) {
	// Email bans are compared in Go, with the same cleaning as the account, so
	// that the rule has one definition. A blank address matches no ban.
	rows, err := s.q.Query(ctx, `
select kind, email, reason_shown from bans
 where (ends_at is null or ends_at > $1)
   and ((kind = 'user' and user_id = $2)
     or (kind = 'email')
     or (kind = 'ip' and $3::inet is not null and ip >>= $3::inet))
 order by created_at, id`, now, u.id, inetArg(ip))
	if err != nil {
		return "", false, fmt.Errorf("login: bans: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			kind, reason string
			email        *string
		)
		if err := rows.Scan(&kind, &email, &reason); err != nil {
			return "", false, fmt.Errorf("login: bans: %w", err)
		}
		if kind == "email" {
			if email == nil || u.emailClean == "" || account.CleanEmail(*email) != u.emailClean {
				continue
			}
		}
		return reason, true, nil
	}
	if err := rows.Err(); err != nil {
		return "", false, fmt.Errorf("login: bans: %w", err)
	}
	return "", false, nil
}

// inetArg returns ip as a query argument: the address without a zone or a
// 4-in-6 form, or nil for the zero address.
func inetArg(ip netip.Addr) any {
	if !ip.IsValid() {
		return nil
	}
	return ip.Unmap().WithZone("")
}

// rehash replaces the stored hash of u with a gobb hash of pw. The update names
// the old hash, so that two sign-ins at once, or a password change in between,
// do not overwrite a newer hash. A failure is logged and does not fail the
// sign-in: the member has proved the password.
func (s *Service) rehash(ctx context.Context, u candidate, pw string) {
	fresh, err := password.Hash(pw)
	if err != nil {
		slog.Warn("login: cannot rehash a password", "user", u.id, "err", err)
		return
	}
	_, err = s.q.Exec(ctx, `
update users set password_hash = $2, password_scheme = 'gobb'
 where id = $1 and password_hash = $3`, u.id, fresh, u.hash)
	if err != nil {
		slog.Warn("login: cannot store a rehashed password", "user", u.id, "err", err)
	}
}

var (
	burnOnce sync.Once
	burnHash string
)

// burn does the work of one password check against a made-up hash, so that a
// name that matches no account, or an account with no password, takes about as
// long as one that does. Without it, the time to answer would tell a stranger
// which names exist.
func burn(pw string) {
	burnOnce.Do(func() {
		h, err := password.Hash("gobb burn password")
		if err != nil {
			slog.Error("login: cannot make the dummy hash", "err", err)
			return
		}
		burnHash = h
	})
	if burnHash != "" {
		password.Verify(pw, burnHash, "gobb")
	}
}
