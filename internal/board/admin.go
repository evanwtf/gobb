package board

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/account"
	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/internal/password"
	"github.com/evanwtf/gobb/internal/session"
)

// The writes of the command line: a password reset, a ban, an unban, an activate
// and a deactivate. They
// belong to the administrator who runs `gobb user ...`, not to a viewer, so they
// take a member ID and no [Viewer]. Each one writes one mod_log row with a null
// actor and the details {"via":"cli"}, and only when it changed something.

const (
	// passwordAlphabet is the alphabet of a generated password: the letters and
	// digits without the look-alike ones (0 and O, 1, l and I, and also i and o).
	// 55 characters, so 16 of them hold about 92 bits.
	passwordAlphabet = lowerLetters + upperLetters + digits

	// The three parts of the alphabet. Lower case drops i, l and o; upper case
	// drops I and O; the digits drop 0 and 1.
	lowerLetters = "abcdefghjkmnpqrstuvwxyz"
	upperLetters = "ABCDEFGHJKLMNPQRSTUVWXYZ"
	digits       = "23456789"
	// generatedPasswordLen is the length of a generated password.
	generatedPasswordLen = 16
	// maxBanReason is the longest ban reason, in characters.
	maxBanReason = 255
	// viaCLI is the details of every mod_log row that the command line writes.
	viaCLI = `{"via":"cli"}`
)

var (
	// ErrAlreadyBanned means that [BanUser] added no ban: a user ban that has not
	// ended holds the member already. Nothing was written.
	ErrAlreadyBanned = errors.New("board: the member is already banned")
	// ErrNotBanned means that [UnbanUser] removed nothing: no user ban that has
	// not ended holds the member. Nothing was written.
	ErrNotBanned = errors.New("board: the member has no ban")
	// ErrAlreadyActive means that [ActivateUser] changed nothing: the account is
	// active. Nothing was written.
	ErrAlreadyActive = errors.New("board: the account is already active")
	// ErrAlreadyInactive means that [DeactivateUser] changed nothing: the account
	// is inactive. Nothing was written.
	ErrAlreadyInactive = errors.New("board: the account is already inactive")
)

// newPassword returns a password of 16 characters, each drawn from
// [passwordAlphabet] with crypto/rand, uniformly (no modulo bias).
func newPassword() (string, error) {
	n := big.NewInt(int64(len(passwordAlphabet)))
	out := make([]byte, generatedPasswordLen)
	for i := range out {
		k, err := rand.Int(rand.Reader, n)
		if err != nil {
			return "", fmt.Errorf("board: draw a password: %w", err)
		}
		out[i] = passwordAlphabet[k.Int64()]
	}
	return string(out), nil
}

// UserByName returns the ID and the stored username of the member whose
// username_clean equals [account.CleanUsername] of name, whatever the state of
// the account. The username lets a command show which member the folded name
// reached. No such member, and a name that no username can be (not valid UTF-8,
// or with a NUL), give [ErrNotFound]. It takes no lock: it runs outside the
// transaction of the write that follows, which locks the row.
func UserByName(ctx context.Context, q db.Querier, name string) (int64, string, error) {
	if !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
		return 0, "", ErrNotFound
	}
	clean := account.CleanUsername(name)
	if clean == "" {
		return 0, "", ErrNotFound
	}
	var (
		id       int64
		username string
	)
	err := q.QueryRow(ctx, `select id, username from users where username_clean = $1`, clean).Scan(&id, &username)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", ErrNotFound
	}
	if err != nil {
		return 0, "", fmt.Errorf("board: find member %q: %w", clean, err)
	}
	return id, username, nil
}

// lockUser takes the user row FOR UPDATE and returns [ErrNotFound] when there is
// none. Every write here that ends sessions or decides on a ban starts with it:
// a sign-in checks the epoch and the bans under FOR SHARE on the same row
// ([session.CreateChecked]), so each pair runs in one order. If the sign-in
// holds the row, the write waits and then deletes its new session; if the write
// holds it, the sign-in waits and then sees the new epoch or the ban.
func lockUser(ctx context.Context, tx pgx.Tx, userID int64) error {
	var id int64
	err := tx.QueryRow(ctx, `select id from users where id = $1 for update`, userID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("board: lock user %d: %w", userID, err)
	}
	return nil
}

// ResetPassword gives member userID a new random password and returns it. The
// password has 16 characters from an alphabet without look-alike characters. It
// is stored as a gobb hash. In one transaction, under FOR UPDATE on the user row,
// the function adds one to the password epoch, writes the hash, deletes every
// session of the member and writes one mod_log row ("user.reset_password", null
// actor, {"via":"cli"}). A member who does not exist is [ErrNotFound], and
// nothing changes.
//
// The password goes to the caller and nowhere else: it is in no log, no error
// and no mod_log row. The hash runs before the transaction opens, so the row
// lock is held for milliseconds.
func ResetPassword(ctx context.Context, pool *pgxpool.Pool, userID int64, now time.Time) (string, error) {
	pw, hash, err := newHashedPassword()
	if err != nil {
		return "", err
	}
	err = inTx(ctx, pool, func(tx pgx.Tx) error {
		if err := storePassword(ctx, tx, userID, hash); err != nil {
			return err
		}
		return logMod(ctx, tx, Viewer{}, "user.reset_password", "user", userID, viaCLI, now)
	})
	if err != nil {
		return "", err
	}
	return pw, nil
}

// storePassword takes the user row FOR UPDATE, adds one to the password epoch,
// stores hash as a gobb hash and deletes every session of member userID. A
// member who does not exist is [ErrNotFound].
func storePassword(ctx context.Context, tx pgx.Tx, userID int64, hash string) error {
	if err := lockUser(ctx, tx, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
update users set password_hash = $2, password_scheme = 'gobb', password_epoch = password_epoch + 1
 where id = $1`, userID, hash); err != nil {
		return fmt.Errorf("board: store a password for %d: %w", userID, err)
	}
	_, err := session.DeleteUser(ctx, tx, userID)
	return err
}

// newHashedPassword returns a new password and its gobb hash. The hash runs
// before any transaction opens, so no row lock waits on it.
func newHashedPassword() (pw, hash string, err error) {
	if pw, err = newPassword(); err != nil {
		return "", "", err
	}
	if hash, err = password.Hash(pw); err != nil {
		// The error of Hash holds no password, and this wrapping adds none.
		return "", "", fmt.Errorf("board: hash a new password: %w", err)
	}
	return pw, hash, nil
}

// BanUser adds a user ban with no end date for member userID, ends every session
// of the member, removes the addresses that the member used (user_ips, spec 10)
// and writes one mod_log row ("user.ban", null actor, {"via":"cli"}), all in one
// transaction under FOR UPDATE on the user row.
// reason is for staff (the column reason); the banned member is not shown it.
//
// A member who does not exist is [ErrNotFound]. A member whom a user ban that has
// not ended holds already gets [ErrAlreadyBanned], and nothing is written. A
// reason that is not valid UTF-8, that holds a NUL, or that is over 255
// characters is an [*InputError] on the field "reason".
func BanUser(ctx context.Context, pool *pgxpool.Pool, userID int64, reason string, now time.Time) error {
	if err := checkBanText("reason", reason); err != nil {
		return err
	}
	return inTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := banUserTx(ctx, tx, userBan{userID: userID, reason: reason}, now); err != nil {
			return err
		}
		return logMod(ctx, tx, Viewer{}, "user.ban", "user", userID, viaCLI, now)
	})
}

// checkBanText returns an [*InputError] on field when text is not valid UTF-8,
// holds a NUL, or is over 255 characters. Both ban reasons use it.
func checkBanText(field, text string) error {
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return inputError(field, "The reason must be valid text.")
	}
	if utf8.RuneCountInString(text) > maxBanReason {
		return inputError(field, "The reason is too long.")
	}
	return nil
}

// userBan is what [banUserTx] writes.
type userBan struct {
	userID int64
	// reason is for staff. shown is the reason that the member reads at sign-in.
	reason, shown string
	// endsAt is the end of the ban, or nil for a ban with no end.
	endsAt *time.Time
	// createdBy is the admin who adds the ban, or 0 for the command line.
	createdBy int64
	// refuseOwner makes a ban of an owner an [*InputError] on the field "target".
	refuseOwner bool
}

// banUserTx is the one write of a user ban, for the command line and for the
// admin pages. In the transaction tx it takes the user row FOR UPDATE, inserts
// the ban, ends every session of the member and removes the addresses that the
// member used. It returns the ID of the ban. It writes no mod_log row: the caller
// writes its own. The text of both reasons is the caller's to check.
//
// A member who does not exist is [ErrNotFound]. A member whom a user ban that has
// not ended holds already gets [ErrAlreadyBanned].
func banUserTx(ctx context.Context, tx pgx.Tx, b userBan, now time.Time) (int64, error) {
	if err := lockUser(ctx, tx, b.userID); err != nil {
		return 0, err
	}
	if b.refuseOwner {
		var owner bool
		if err := tx.QueryRow(ctx, `select is_owner from users where id = $1`, b.userID).Scan(&owner); err != nil {
			return 0, fmt.Errorf("board: read the owner flag of %d: %w", b.userID, err)
		}
		if owner {
			return 0, inputError("target", "An owner cannot be banned.")
		}
	}
	var banned bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from bans where kind = 'user' and user_id = $1 and (ends_at is null or ends_at > $2))`,
		b.userID, now).Scan(&banned); err != nil {
		return 0, fmt.Errorf("board: check the bans of %d: %w", b.userID, err)
	}
	if banned {
		return 0, ErrAlreadyBanned
	}
	var id int64
	if err := tx.QueryRow(ctx, `
insert into bans (kind, user_id, reason, reason_shown, created_at, ends_at, created_by)
values ('user', $1, $2, $3, $4, $5, $6) returning id`,
		b.userID, b.reason, b.shown, now, b.endsAt, nullID(b.createdBy)).Scan(&id); err != nil {
		return 0, fmt.Errorf("board: ban %d: %w", b.userID, err)
	}
	if _, err := session.DeleteUser(ctx, tx, b.userID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `delete from user_ips where user_id = $1`, b.userID); err != nil {
		return 0, fmt.Errorf("board: remove the addresses of %d: %w", b.userID, err)
	}
	return id, nil
}

// UnbanUser removes the user bans of member userID that have not ended, and
// writes one mod_log row ("user.unban", null actor, {"via":"cli"}), in one
// transaction under FOR UPDATE on the user row. A ban that has ended stays as
// history, as do bans of other kinds. A member who does not exist is
// [ErrNotFound]. A member with no such ban gets [ErrNotBanned], and nothing is
// written.
func UnbanUser(ctx context.Context, pool *pgxpool.Pool, userID int64, now time.Time) error {
	return inTx(ctx, pool, func(tx pgx.Tx) error {
		if err := lockUser(ctx, tx, userID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `delete from bans where kind = 'user' and user_id = $1 and (ends_at is null or ends_at > $2)`, userID, now)
		if err != nil {
			return fmt.Errorf("board: unban %d: %w", userID, err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotBanned
		}
		return logMod(ctx, tx, Viewer{}, "user.unban", "user", userID, viaCLI, now)
	})
}

// ActivateUser makes the account of member userID active, and writes one mod_log
// row ("user.activate", null actor, {"via":"cli"}), in one transaction under FOR
// UPDATE on the user row. The member can then sign in and ask for a reset email.
// The password, the email address and the bans stay as they are, so a banned
// member stays banned. A member who does not exist is [ErrNotFound]. An account
// that is active already gets [ErrAlreadyActive], and nothing is written.
func ActivateUser(ctx context.Context, pool *pgxpool.Pool, userID int64, now time.Time) error {
	return inTx(ctx, pool, func(tx pgx.Tx) error {
		if err := setActive(ctx, tx, userID, true); err != nil {
			return err
		}
		return logMod(ctx, tx, Viewer{}, "user.activate", "user", userID, viaCLI, now)
	})
}

// setActive takes the user row FOR UPDATE and makes the account of member userID
// active or inactive. Making it inactive also deletes every session of the
// member. A member who does not exist is [ErrNotFound]; an account in that state
// already is [ErrAlreadyActive] or [ErrAlreadyInactive].
func setActive(ctx context.Context, tx pgx.Tx, userID int64, active bool) error {
	if err := lockUser(ctx, tx, userID); err != nil {
		return err
	}
	state, same := "active", ErrAlreadyActive
	if !active {
		state, same = "inactive", ErrAlreadyInactive
	}
	tag, err := tx.Exec(ctx, `update users set state = $2 where id = $1 and state <> $2`, userID, state)
	if err != nil {
		return fmt.Errorf("board: make %d %s: %w", userID, state, err)
	}
	if tag.RowsAffected() == 0 {
		return same
	}
	if !active {
		if _, err := session.DeleteUser(ctx, tx, userID); err != nil {
			return err
		}
	}
	return nil
}

// DeactivateUser makes the account of member userID inactive, ends every session
// of the member and writes one mod_log row ("user.deactivate", null actor,
// {"via":"cli"}), all in one transaction under FOR UPDATE on the user row. An
// inactive member cannot sign in, gets no reset email, and a reset link sent
// earlier stops working. The posts, the password and the email address stay, so
// [ActivateUser] gives the same sign-in back. A member who does not exist is
// [ErrNotFound]. An account that is inactive already gets [ErrAlreadyInactive],
// and nothing is written.
func DeactivateUser(ctx context.Context, pool *pgxpool.Pool, userID int64, now time.Time) error {
	return inTx(ctx, pool, func(tx pgx.Tx) error {
		if err := setActive(ctx, tx, userID, false); err != nil {
			return err
		}
		return logMod(ctx, tx, Viewer{}, "user.deactivate", "user", userID, viaCLI, now)
	})
}
