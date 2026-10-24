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
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
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
// Check returns it before any password check. When only the limit of the name is
// full, a lookup of the name and one query on user_ips run first, to see whether
// the account has used the address; the same two run whether or not an account
// has the name. No password hash runs.
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

// DefaultFloor is the least floor of a failed check (see [Service.Floor]).
// [Service.LoadFloor] raises the floor above it when the checks of this host
// need more.
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
	// DefaultBudgetKiB is the most memory that password checks may use
	// together: 128 MiB. [BudgetKiB] gives less under a lower memory limit.
	DefaultBudgetKiB = 128 * 1024
	// DefaultGateWait is how long a check waits for the gate before ErrBusy.
	DefaultGateWait = 5 * time.Second
)

// gateHeadroomKiB is the memory that the gate leaves to the rest of the server
// under a memory limit: 48 MiB for the pages, the pool and the runtime.
const gateHeadroomKiB = 48 * 1024

// minBudgetKiB is the least gate budget. Any budget runs at least one check,
// because Acquire clamps a larger cost to the budget.
const minBudgetKiB = 1024

// BudgetKiB returns the gate budget, in KiB, for a process whose Go memory
// limit is limit bytes: min(DefaultBudgetKiB, limit − 48 MiB), and at least
// 1 MiB. A limit of 0 or less means no limit, and gives DefaultBudgetKiB.
//
// The Go runtime lets the heap grow to about twice the live heap before it
// collects, unless a memory limit makes it collect sooner. So the memory
// limit, not the gate alone, keeps a burst of checks under a container's cap.
// The gate keeps the live heap of the checks under the limit.
func BudgetKiB(limit int64) uint64 {
	if limit <= 0 {
		return DefaultBudgetKiB
	}
	kib := uint64(limit) / 1024
	if kib <= gateHeadroomKiB+minBudgetKiB {
		return minBudgetKiB
	}
	return min(DefaultBudgetKiB, kib-gateHeadroomKiB)
}

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
	// Floor is the least time that a failed [Service.Check] takes, counted from
	// its start, moved forward by the time that it waits for the gate. A gate
	// wait then adds the same delay to every name, and the floor still hides the
	// lookup and the hash work. The lookup is not part of the wait: a missing
	// name costs one query more than an existing one. A failure is any return
	// except a good password and a [*LimitedError]: a wrong password, an
	// ambiguous address, a busy gate, an inactive account, a ban and an error.
	// The hashes of the imported board differ a lot in cost, and an unknown name
	// costs one fast check, so without a floor the time to answer would tell a
	// stranger whether a name or an address has an account. The wait comes after
	// the gate is free again.
	//
	// New sets it to [DefaultFloor]. [Service.LoadFloor] sets it at start to
	// max(DefaultFloor, 2 × K × T), rounded up to a whole 50 ms: T is the
	// slowest single wrong-password check on this host, and K is the most
	// accounts that share one email address, so the most checks that one sign-in
	// runs. The floor hides the hash time while the host runs a check within 2×
	// of its start-up time. Heavier CPU load can still show the time. A test may
	// set Floor directly and skip LoadFloor. Zero means no floor.
	Floor time.Duration
	// GateWait is how long a check waits for room in the gate. New sets it to
	// [DefaultGateWait].
	GateWait time.Duration
	// stored is the cost of the most costly stored hash, in KiB, that
	// LoadCharge found. Zero until LoadCharge runs.
	stored atomic.Uint64
	// hash makes the gobb hash of a rehash. A test may replace it.
	hash func(string) (string, error)
	// onAcquire, when set, sees each charge that the service asks of the gate.
	// A test sets it.
	onAcquire func(kib uint64)
	// timeVerify, when set, replaces the timing of one check at start
	// (LoadFloor). A test sets it, to control T without real hashes.
	timeVerify func(stored, scheme string) time.Duration
}

// clock is the time that the floor of one sign-in counts from: the start of
// the sign-in, moved forward by the time that the sign-in waits for the gate.
type clock struct{ from time.Time }

// waited moves the clock forward by d, the time that the sign-in waited for the
// gate. The lookup before the wait does not move it.
func (c *clock) waited(d time.Duration) {
	if d > 0 {
		c.from = c.from.Add(d)
	}
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
	return &Service{q: q, opt: opt, Floor: DefaultFloor, GateWait: DefaultGateWait, hash: password.Hash}
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
// it runs no password check and does not wait for the floor. A name with no
// account is limited like one with an account. A try that the address limit
// refuses takes nothing from the name limit. A try that the name limit refuses
// keeps its address event, so one address cannot send unlimited lookups at a
// locked name.
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
// A sign-in takes the gate once, before any hash, and holds it across all of
// its hash work: the check of each account that the name finds (several for a
// shared email address), or the dummy check for a name with no account. The
// hold is the largest charge among them ([Service.ChargeKiB]). So a gate wait,
// [ErrBusy] and a cancel of ctx happen once, at the same point, for every name.
// When the gate stays full, Check returns [ErrBusy].
//
// A wrong password or an ambiguous address keeps the two reserved events. Any
// other end gives them back: a good password, [ErrBusy], [ErrInactive], a ban, a
// context or a database error. A good password also clears the name.
//
// Every return after the reservation, except a good password, comes no sooner
// than Floor after the call began, plus the time that it waited for the gate,
// whatever happens to ctx: a wrong password, an ambiguous address,
// [ErrBusy], a gate wait that ctx ended, [ErrInactive], a ban, and a lookup or
// database error. Otherwise the time to answer would tell a stranger which check
// ran. The wait comes after the gate is released. A [*LimitedError] and a good
// password return at once.
func (s *Service) Check(ctx context.Context, name, pw string, ip netip.Addr, now time.Time) (int64, error) {
	c := &clock{from: time.Now()}
	k := s.keys(name, ip)
	held, err := s.reserve(ctx, name, k, now)
	if err != nil {
		return 0, err
	}
	id, err := s.check(ctx, c, name, pw, ip, now)
	if err == nil {
		s.refund(held, k, now, false)
		if s.opt.NameLimit != nil && k.nameOK {
			s.opt.NameLimit.Reset(k.name)
		}
		return id, nil
	}
	// Every failure that follows the reservation takes the floor. The events of
	// a wrong password stay; any other end gives them back.
	if !errors.Is(err, ErrWrong) && !errors.Is(err, ErrAmbiguous) {
		s.refund(held, k, now, true)
	}
	s.wait(c.from)
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
// longer wait of the limits that are full. The address limit comes first: when it
// is full, the try takes nothing from the name limit (the name's wait is only
// read). When the name limit is the one that is full, and the client address is
// known to an account with that name, the name limit is skipped. A try that the
// name limit refuses keeps its address event, so that one address cannot send
// unlimited lookups at a locked name. A database error gives the events back.
func (s *Service) reserve(ctx context.Context, name string, k limitKeys, now time.Time) (held, error) {
	var h held
	if s.opt.IPLimit != nil {
		ok, ipWait := s.opt.IPLimit.Take(k.ip, now)
		if !ok {
			wait := ipWait
			if s.opt.NameLimit != nil && k.nameOK {
				if over, w := s.opt.NameLimit.Over(k.name, now); over {
					wait = max(wait, w)
				}
			}
			return held{}, &LimitedError{RetryAfter: wait}
		}
		h.ip = true
	}
	if s.opt.NameLimit != nil && k.nameOK {
		ok, nameWait := s.opt.NameLimit.Take(k.name, now)
		if ok {
			h.name = true
			return h, nil
		}
		known, err := s.knownAddress(ctx, name, k)
		if err != nil {
			s.refund(h, k, now, true)
			return held{}, err
		}
		if !known {
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
// not tell a stranger whether the name exists. The unspecified address (0.0.0.0
// or ::) is never known: it stands for every client whose address could not be
// read.
func (s *Service) knownAddress(ctx context.Context, name string, k limitKeys) (bool, error) {
	users, err := s.find(ctx, name)
	if err != nil {
		return false, err
	}
	if k.prefix.IsValid() && k.prefix.Addr().IsUnspecified() {
		return false, nil
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

// wait sleeps until Floor has passed since from. It does not watch the context
// of the request: net/http cancels that context when the client half-closes its
// connection, and the client can still read the answer, so a wait that ended with
// the context would let a client time the answer to the hash alone. The hold is
// at most Floor.
func (s *Service) wait(from time.Time) {
	left := s.Floor - time.Since(from)
	if left <= 0 {
		return
	}
	time.Sleep(left)
}

// LoadCharge sets the share of the gate that every password check holds (see
// [Service.ChargeKiB]) from the stored hashes. One streaming query reads the
// users whose hash can carry an argon2id part, and [password.MemoryKiB], the
// parser that sizes a check, gives the cost of each. LoadCharge then logs the
// charge and how many checks fit in the gate at once, with no name and no hash.
//
// A stored hash whose cost is above the gate budget gets one WARN line, with the
// number of such accounts and no name. A check of such a hash still runs:
// Acquire clamps its cost to the budget, so it runs alone. But it may need more
// memory than the server has; the README says to raise the memory cap of such a
// board.
//
// Call it once, before the service serves. The value does not change at run
// time: a rehash stores a gobb hash, which never costs more.
func (s *Service) LoadCharge(ctx context.Context) error {
	rows, err := s.q.Query(ctx, `select password_scheme, password_hash from users
 where strpos(password_hash, '$argon2id$') > 0`)
	if err != nil {
		return fmt.Errorf("login: read the stored hashes: %w", err)
	}
	defer rows.Close()
	var most, over uint64
	for rows.Next() {
		var scheme, hash string
		if err := rows.Scan(&scheme, &hash); err != nil {
			return fmt.Errorf("login: read the stored hashes: %w", err)
		}
		cost := password.MemoryKiB(scheme, hash)
		most = max(most, cost)
		if s.opt.Gate != nil && cost > s.opt.Gate.budget {
			over++
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("login: read the stored hashes: %w", err)
	}
	s.stored.Store(most)
	charge := s.ChargeKiB()
	if s.opt.Gate == nil {
		slog.Info("login: password check charge", "kib", charge)
		return nil
	}
	slog.Info("login: password check charge", "kib", charge, "checks_at_once", s.opt.Gate.fits(charge))
	if over > 0 {
		slog.Warn("login: stored password hashes cost more than the gate budget; each check of one runs alone and may pass the memory cap",
			"accounts", over, "budget_kib", s.opt.Gate.budget)
	}
	return nil
}

// floorStep is the unit that LoadFloor rounds the floor up to.
const floorStep = 50 * time.Millisecond

// probePassword is the wrong password that LoadFloor checks. It is no one's.
// It is 4,096 bytes long, the longest that Verify reads, and its legacy form
// has the same length: a chain check grows with the length of the password,
// and a stranger chooses the length.
var probePassword = strings.Repeat("gobb-floor-probe", 4096/len("gobb-floor-probe"))

// LoadFloor times the password checks of this host and sets [Service.Floor] to
// max(DefaultFloor, 2 × K × T), rounded up to a whole 50 ms.
//   - K is the most accounts that share one email_clean, or 1 when none share:
//     the most checks that one sign-in runs.
//   - T is the slowest single wrong-password check. One streaming query reads
//     the stored hashes, and [password.CostShape] sorts them by format and cost
//     parameters. LoadFloor times one check against one hash of each shape,
//     and of the dummy check (the shape of a new gobb hash too).
//
// Each check runs with a wrong password of 4,096 bytes, the longest that
// Verify reads, and holds the gate with the charge of a check of that hash
// (chargeKiB), as a sign-in does. After the last check, LoadFloor runs a
// garbage collection, so the memory of the checks (and of the dummy hash) does
// not sit under the first sign-ins.
//
// It logs one INFO line with the floor in ms, T in ms and K, with no name and
// no hash. Call it once, after [Service.LoadCharge] and before the service
// serves.
func (s *Service) LoadFloor(ctx context.Context) error {
	var k int64
	err := s.q.QueryRow(ctx, `select coalesce(max(n), 1) from (
select count(*) as n from users where email_clean <> '' group by email_clean) shared`).Scan(&k)
	if err != nil {
		return fmt.Errorf("login: count shared email addresses: %w", err)
	}
	type sample struct{ stored, scheme string }
	shapes := map[string]sample{}
	if h := dummyHash(); h != "" {
		shapes[password.CostShape("gobb", h)] = sample{h, "gobb"}
	}
	rows, err := s.q.Query(ctx, `select password_scheme, password_hash from users where password_hash <> ''`)
	if err != nil {
		return fmt.Errorf("login: read the stored hashes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var scheme, hash string
		if err := rows.Scan(&scheme, &hash); err != nil {
			return fmt.Errorf("login: read the stored hashes: %w", err)
		}
		// A hash that Verify refuses before any work has no shape and no cost.
		if shape := password.CostShape(scheme, hash); shape != "" {
			if _, seen := shapes[shape]; !seen {
				shapes[shape] = sample{hash, scheme}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("login: read the stored hashes: %w", err)
	}
	var slowest time.Duration
	for _, sm := range shapes {
		release, err := s.acquire(ctx, s.chargeKiB(sm.scheme, sm.stored))
		if err != nil {
			return fmt.Errorf("login: time a password check: %w", err)
		}
		took := s.timeOne(sm.stored, sm.scheme)
		release()
		slowest = max(slowest, took)
	}
	runtime.GC()
	floor := max(DefaultFloor, 2*time.Duration(k)*slowest)
	floor = (floor + floorStep - 1) / floorStep * floorStep
	s.Floor = floor
	slog.Info("login: sign-in floor", "floor_ms", floor.Milliseconds(),
		"t_ms", slowest.Round(time.Millisecond).Milliseconds(), "k", k, "shapes", len(shapes))
	return nil
}

// timeOne returns how long one wrong-password check against stored takes.
func (s *Service) timeOne(stored, scheme string) time.Duration {
	if s.timeVerify != nil {
		return s.timeVerify(stored, scheme)
	}
	start := time.Now()
	s.opt.Verify(probePassword, stored, scheme)
	return time.Since(start)
}

// ChargeKiB returns the share of the gate, in KiB, that every password check
// holds: the larger of the memory of a new gobb hash ([password.HashMemoryKiB])
// and the memory of the most costly stored hash that [Service.LoadCharge] found.
// Before LoadCharge runs, it is the memory of a new gobb hash.
//
// The charge follows the most costly stored hash. Imported argon2id hashes
// commonly use m=65,536, so on a board that holds one the charge is 65,536 KiB,
// and two checks run at once in the default 128 MiB gate.
func (s *Service) ChargeKiB() uint64 {
	return max(password.HashMemoryKiB, s.stored.Load())
}

// chargeKiB is what one password check holds of the gate: [Service.ChargeKiB],
// or the cost of the stored hash when that is higher. Every gate charge in this
// package comes from here: the hash check, the dummy check for an unknown name
// and the rehash.
//
// The charge is the same for every hash on the board, so a legacy account
// (bcrypt or a chain at 1,024 KiB, argon2id at its m), a gobb account and an
// unknown name all hold the same share. Under a full gate they wait alike and
// end alike, so neither the time nor the status of the answer tells a stranger
// whether an account exists or what kind of hash it holds.
//
// The one residual: a hash stored after LoadCharge ran, with a cost above the
// charge, holds its own cost. Only an import into the database of a running
// server stores such a hash; the next start counts it.
func (s *Service) chargeKiB(scheme, stored string) uint64 {
	return max(s.ChargeKiB(), password.MemoryKiB(scheme, stored))
}

// hashJob is one password check of a sign-in: a stored hash and its scheme.
// A dummy job checks the made-up dummy hash; its result never counts.
type hashJob struct {
	stored, scheme string
	dummy          bool
	ok, rehash     bool
}

// dummyJob returns the job of one dummy check.
func dummyJob() hashJob {
	return hashJob{stored: dummyHash(), scheme: "gobb", dummy: true}
}

// runJobs checks pw against every job in one hold of the gate. The hold is the
// largest charge among the jobs (chargeKiB), taken once before the first hash
// and given back after the last. The floor clock c moves forward by the time
// that the sign-in waited for the gate (granted, busy or ended by ctx), and by
// nothing else: the lookup before the wait stays under the floor. Before each check after the first, runJobs
// collects garbage, so the memory of a finished check is free before the next
// one starts, as the gate budget assumes. A dummy job with no dummy hash
// (password.Hash failed at its first use) does no work.
func (s *Service) runJobs(ctx context.Context, c *clock, pw string, jobs []hashJob) error {
	var kib uint64
	for _, j := range jobs {
		kib = max(kib, s.chargeKiB(j.scheme, j.stored))
	}
	asked := time.Now()
	release, err := s.acquire(ctx, kib)
	// The wait counts whether or not the gate grants room: a busy answer and a
	// wait that ctx ended come at the same point for every name.
	c.waited(time.Since(asked))
	if err != nil {
		return err
	}
	defer release()
	ran := false
	for i := range jobs {
		j := &jobs[i]
		if j.stored == "" {
			continue
		}
		if ran {
			runtime.GC()
		}
		ran = true
		ok, rehash := s.opt.Verify(pw, j.stored, j.scheme)
		if !j.dummy {
			j.ok, j.rehash = ok, rehash
		}
	}
	return nil
}

// acquire takes kib from the gate. With no gate, it takes nothing.
//
// The release runs a garbage collection first, then gives the share back. The
// memory of a check is garbage when the check ends, and the runtime collects
// it only after a later allocation, even under a memory limit. Without the
// collection, the next holder's memory comes on top of that garbage: with
// 64 MiB hashes, the heap held three 64 MiB blocks where the gate allows two,
// and the kernel killed a container under a 256 MiB cap. The collection is
// cheap, because the memory of a hash holds no pointers: well under 1 ms with
// two 64 MiB checks live. It runs inside the floor.
func (s *Service) acquire(ctx context.Context, kib uint64) (func(), error) {
	if s.onAcquire != nil {
		s.onAcquire(kib)
	}
	if s.opt.Gate == nil {
		return func() {}, nil
	}
	release, err := s.opt.Gate.Acquire(ctx, kib, s.GateWait)
	if err != nil {
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			runtime.GC()
			release()
		})
	}, nil
}

func (s *Service) check(ctx context.Context, c *clock, name, pw string, ip netip.Addr, now time.Time) (int64, error) {
	if !utf8.ValidString(name) || len(name) > maxName {
		if err := s.runJobs(ctx, c, pw, []hashJob{dummyJob()}); err != nil {
			return 0, err
		}
		return 0, ErrWrong
	}
	users, err := s.find(ctx, name)
	if err != nil {
		return 0, err
	}
	if len(users) == 0 {
		if err := s.runJobs(ctx, c, pw, []hashJob{dummyJob()}); err != nil {
			return 0, err
		}
		return 0, ErrWrong
	}

	// One job per account, in one hold of the gate. An account with no hash
	// runs the dummy check and never matches.
	jobs := make([]hashJob, len(users))
	for i, u := range users {
		jobs[i] = hashJob{stored: u.hash, scheme: u.scheme}
		if u.hash == "" {
			jobs[i] = dummyJob()
		}
	}
	if err := s.runJobs(ctx, c, pw, jobs); err != nil {
		return 0, err
	}
	matches := 0
	for i := range users {
		users[i].matched, users[i].needsRehash = jobs[i].ok, jobs[i].rehash
		if users[i].matched {
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
	release, err := s.acquire(ctx, s.chargeKiB("gobb", ""))
	if err != nil {
		slog.Warn("login: skipped a rehash", "user", u.id, "err", err)
		return
	}
	fresh, err := s.hash(pw)
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
	dummyOnce sync.Once
	dummy     string
)

// dummyHash returns a made-up gobb hash, made once. A sign-in checks the
// password against it when the name matches no account, or an account with no
// password, so that such a name takes about as long as one that does. Without
// it, the time to answer would tell a stranger which names exist. It is "" when
// password.Hash fails.
func dummyHash() string {
	dummyOnce.Do(func() {
		h, err := password.Hash("gobb burn password")
		if err != nil {
			slog.Error("login: cannot make the dummy hash", "err", err)
			return
		}
		dummy = h
	})
	return dummy
}
