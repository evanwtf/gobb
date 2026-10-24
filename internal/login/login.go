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
	"github.com/evanwtf/gobb/internal/ratelimit"
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

// LimitedError means a limit on failed sign-ins holds the address or the name.
// Check returns it before any lookup or hash.
type LimitedError struct {
	// RetryAfter is how long until the longer of the two limits has room again.
	RetryAfter time.Duration
}

// Error implements error.
func (e *LimitedError) Error() string {
	return "login: too many failed sign-ins; retry in " + e.RetryAfter.Round(time.Second).String()
}

// maxName is the longest name that Check looks up, in bytes. No username or
// email address is longer. The limit keeps the cleaning cheap.
const maxName = 512

// DefaultFloor is the least time that a failed check takes.
const DefaultFloor = 400 * time.Millisecond

// The defaults of [New]. Spec 19 leaves the numbers to the builder.
const (
	// DefaultIPFailures is how many failed sign-ins one address may make in
	// DefaultIPWindow.
	DefaultIPFailures = 10
	// DefaultIPWindow is the window of the limit per address.
	DefaultIPWindow = 15 * time.Minute
	// DefaultNameFailures is how many failed sign-ins one name may take in
	// DefaultNameWindow.
	DefaultNameFailures = 20
	// DefaultNameWindow is the window of the limit per name.
	DefaultNameWindow = time.Hour
	// DefaultMaxKeys is the most keys that each limiter holds.
	DefaultMaxKeys = 10000
	// DefaultBudgetKiB is the memory that password checks may use together: 128 MiB.
	DefaultBudgetKiB = 128 * 1024
	// DefaultGateWait is how long a check waits for the gate before ErrBusy.
	DefaultGateWait = 5 * time.Second
)

// Options sets up a [Service]. A nil field turns that part off: a nil limiter
// does not limit, and a nil Gate does not hold checks back. A nil Verify is
// [password.Verify].
type Options struct {
	// IPLimit counts the failed sign-ins of each client address.
	IPLimit *ratelimit.Limiter
	// NameLimit counts the failed sign-ins of each cleaned name, whether or not
	// an account has the name.
	NameLimit *ratelimit.Limiter
	// Gate limits the memory of the password checks that run at one time.
	Gate *Gate
	// Verify checks a password against a stored hash. A test may replace it.
	Verify func(raw, stored, scheme string) (ok, needsRehash bool)
}

// Service checks sign-ins.
type Service struct {
	q   db.Querier
	opt Options
	// Floor is the least time that a failed [Service.Check] (ErrWrong or
	// ErrAmbiguous) takes, counted from its start. The hashes of the imported
	// board differ a lot in cost, and an unknown name costs one fast check, so
	// without a floor the time to answer would tell a stranger whether a name or
	// an address has an account. New sets it to [DefaultFloor]; a test may set it
	// small. Zero means no floor. The wait comes after the gate is free again.
	Floor time.Duration
	// GateWait is how long a check waits for room in the gate. New sets it to
	// [DefaultGateWait].
	GateWait time.Duration
}

// DefaultOptions returns new limiters and a new gate with the default numbers,
// and [password.Verify]. Each call returns its own limiters and gate, so two
// services share no counts.
func DefaultOptions() Options {
	return Options{
		IPLimit:   ratelimit.New(DefaultIPFailures, DefaultIPWindow, DefaultMaxKeys),
		NameLimit: ratelimit.New(DefaultNameFailures, DefaultNameWindow, DefaultMaxKeys),
		Gate:      NewGate(DefaultBudgetKiB),
		Verify:    password.Verify,
	}
}

// New returns a Service that reads and writes through q, set up by
// [DefaultOptions].
func New(q db.Querier) *Service {
	return NewWith(q, DefaultOptions())
}

// NewWith returns a Service that reads and writes through q, set up by opt.
func NewWith(q db.Querier, opt Options) *Service {
	if opt.Verify == nil {
		opt.Verify = password.Verify
	}
	return &Service{q: q, opt: opt, Floor: DefaultFloor, GateWait: DefaultGateWait}
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
// The limits come first, and they are reserved, not only tested: Check takes one
// event from the limit of the address and one from the limit of the cleaned name,
// each in one step under the lock of its limiter, before any hash. Parallel
// guesses therefore cannot pass a limit that the first of them has not reached.
// If either limit is full, Check returns a [*LimitedError] with the longer wait;
// it does no hash and does not wait for the floor. A name with no account is
// limited like one with an account.
//
// One exception: when only the limit of the name is full, and the address of the
// client is one that an account with that name has used before (user_ips), the
// limit of the name does not apply to this try. The limit of the address still
// does. The lookup runs for every name, with or without an account, so a
// stranger sees the same answer either way.
//
// Then the order is fixed, so that the errors tell a stranger nothing:
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
// Each hash check, and each dummy check for a name with no account, holds the
// memory it needs in the gate while it runs. When the gate stays full, Check
// returns [ErrBusy].
//
// A wrong password or an ambiguous address keeps the two reserved events. Any
// other end gives them back: a good password, [ErrBusy], [ErrInactive], a ban, a
// context or a database error. A good password also clears the name. A failure
// that a stranger can cause with a guess (ErrWrong or ErrAmbiguous) returns no
// sooner than Floor after the call began. The wait comes after the gate is
// released, and ends early when ctx ends; the answer does not change.
func (s *Service) Check(ctx context.Context, name, pw string, ip netip.Addr, now time.Time) (int64, error) {
	start := time.Now()
	k := s.keys(name, ip)
	held, err := s.reserve(ctx, name, k, ip, now)
	if err != nil {
		return 0, err
	}
	id, err := s.check(ctx, name, pw, ip, now)
	switch {
	case errors.Is(err, ErrWrong) || errors.Is(err, ErrAmbiguous):
		s.wait(ctx, start)
	case err == nil:
		s.refund(held, k, now, false)
		if s.opt.NameLimit != nil && k.nameOK {
			s.opt.NameLimit.Reset(k.name)
		}
	default:
		s.refund(held, k, now, true)
	}
	return id, err
}

// limitKeys are the keys of one sign-in in the two limiters.
type limitKeys struct {
	ip string
	// prefix is the range of the client that the address limit counts: the
	// address for IPv4, the /64 for IPv6. It is invalid when the address is.
	prefix netip.Prefix
	name   string
	// nameOK is false for a name that is not valid UTF-8 or is too long. Such a
	// name has no key in the name limit, and counts against the address only.
	nameOK bool
}

// held says which events a try has reserved.
type held struct{ ip, name bool }

// keys returns the limiter keys of a sign-in. The address key is the plain
// address for IPv4 (an IPv4-mapped address first becomes the IPv4 address) and
// the /64 for IPv6, since one client holds a whole /64. The name key is the
// cleaned name.
func (s *Service) keys(name string, ip netip.Addr) limitKeys {
	var k limitKeys
	if ip.IsValid() {
		ip = ip.Unmap().WithZone("")
		bits := 64
		if ip.Is4() {
			bits = 32
		}
		k.prefix = netip.PrefixFrom(ip, bits).Masked()
		k.ip = k.prefix.String()
	} else {
		k.ip = "invalid"
	}
	if utf8.ValidString(name) && len(name) <= maxName {
		k.name, k.nameOK = account.CleanUsername(name), true
	}
	return k
}

// reserve takes one event from each limit, or returns a [*LimitedError] with the
// longer wait of the limits that are full. When the limit of the name is the only
// one that is full and the client address is known to an account with that name,
// the name limit is skipped. A database error gives the events back.
func (s *Service) reserve(ctx context.Context, name string, k limitKeys, ip netip.Addr, now time.Time) (held, error) {
	var h held
	var ipWait, nameWait time.Duration
	ipOver, nameOver := false, false
	if s.opt.IPLimit != nil {
		ok, w := s.opt.IPLimit.Take(k.ip, now)
		h.ip, ipOver, ipWait = ok, !ok, w
	}
	if s.opt.NameLimit != nil && k.nameOK {
		ok, w := s.opt.NameLimit.Take(k.name, now)
		h.name, nameOver, nameWait = ok, !ok, w
	}
	if ipOver {
		s.refund(h, k, now, true)
		return held{}, &LimitedError{RetryAfter: max(ipWait, nameWait)}
	}
	if nameOver {
		known, err := s.knownAddress(ctx, name, k)
		if err != nil {
			s.refund(h, k, now, true)
			return held{}, err
		}
		if !known {
			s.refund(h, k, now, true)
			return held{}, &LimitedError{RetryAfter: nameWait}
		}
	}
	return h, nil
}

// refund gives back the events that h holds. The name event goes back only when
// withName is true; a good password clears the whole name instead.
func (s *Service) refund(h held, k limitKeys, now time.Time, withName bool) {
	if h.ip {
		s.opt.IPLimit.Refund(k.ip, now)
	}
	if h.name && withName {
		s.opt.NameLimit.Refund(k.name, now)
	}
}

// knownAddress reports whether an account that name finds has used the client
// range (user_ips). It looks the name up even when no account has it, and runs
// the query for the addresses with an empty list of accounts, so the work does
// not tell a stranger whether the name exists.
func (s *Service) knownAddress(ctx context.Context, name string, k limitKeys) (bool, error) {
	users, err := s.find(ctx, name)
	if err != nil {
		return false, err
	}
	ids := make([]int64, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.id)
	}
	var prefix any
	if k.prefix.IsValid() {
		prefix = k.prefix
	}
	var known bool
	err = s.q.QueryRow(ctx, `select exists (
select 1 from user_ips where user_id = any($1) and ip <<= $2::inet)`, ids, prefix).Scan(&known)
	if err != nil {
		return false, fmt.Errorf("login: known address: %w", err)
	}
	return known, nil
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

// verify checks pw against a stored hash. It holds the memory that the hash
// needs in the gate while the hash runs, and lets go before it returns.
func (s *Service) verify(ctx context.Context, pw, stored, scheme string) (ok, needsRehash bool, err error) {
	release, err := s.acquire(ctx, password.MemoryKiB(scheme, stored))
	if err != nil {
		return false, false, err
	}
	defer release()
	ok, needsRehash = s.opt.Verify(pw, stored, scheme)
	return ok, needsRehash, nil
}

// acquire takes kib from the gate. With no gate, it takes nothing.
func (s *Service) acquire(ctx context.Context, kib uint64) (func(), error) {
	if s.opt.Gate == nil {
		return func() {}, nil
	}
	return s.opt.Gate.Acquire(ctx, kib, s.GateWait)
}

func (s *Service) check(ctx context.Context, name, pw string, ip netip.Addr, now time.Time) (int64, error) {
	if !utf8.ValidString(name) || len(name) > maxName {
		if err := s.burn(ctx, pw); err != nil {
			return 0, err
		}
		return 0, ErrWrong
	}
	users, err := s.find(ctx, name)
	if err != nil {
		return 0, err
	}
	if len(users) == 0 {
		if err := s.burn(ctx, pw); err != nil {
			return 0, err
		}
		return 0, ErrWrong
	}

	matches := 0
	for i := range users {
		u := &users[i]
		if u.hash == "" {
			if err := s.burn(ctx, pw); err != nil {
				return 0, err
			}
			continue
		}
		u.matched, u.needsRehash, err = s.verify(ctx, pw, u.hash, u.scheme)
		if err != nil {
			return 0, err
		}
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

// rehash replaces the stored hash of u with a gobb hash of pw. The hash runs
// inside the gate. The update names the old hash, so that two sign-ins at once, or a password change in between,
// do not overwrite a newer hash. A failure is logged and does not fail the
// sign-in: the member has proved the password.
func (s *Service) rehash(ctx context.Context, u candidate, pw string) {
	// The hash needs memory like any check, so it holds the gate. When the gate
	// stays full, skip: the password is right, and the next sign-in tries again.
	release, err := s.acquire(ctx, password.HashMemoryKiB)
	if err != nil {
		slog.Warn("login: skipped a rehash", "user", u.id, "err", err)
		return
	}
	fresh, err := password.Hash(pw)
	release()
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

// burn does the work of one password check against a made-up gobb hash, so that
// a name that matches no account, or an account with no password, takes about as
// long as one that does. Without it, the time to answer would tell a stranger
// which names exist. It runs through the same gate and the same Verify as a real
// check. It is the one dummy check.
func (s *Service) burn(ctx context.Context, pw string) error {
	burnOnce.Do(func() {
		h, err := password.Hash("gobb burn password")
		if err != nil {
			slog.Error("login: cannot make the dummy hash", "err", err)
			return
		}
		burnHash = h
	})
	if burnHash == "" {
		return nil
	}
	_, _, err := s.verify(ctx, pw, burnHash, "gobb")
	return err
}
