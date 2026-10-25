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

// The writes of the command line: a password reset, a ban and an unban. They
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
	pw, err := newPassword()
	if err != nil {
		return "", err
	}
	hash, err := password.Hash(pw)
	if err != nil {
		// The error of Hash holds no password, and this wrapping adds none.
		return "", fmt.Errorf("board: hash a new password: %w", err)
	}
	err = inTx(ctx, pool, func(tx pgx.Tx) error {
		if err := lockUser(ctx, tx, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
update users set password_hash = $2, password_scheme = 'gobb', password_epoch = password_epoch + 1
 where id = $1`, userID, hash); err != nil {
			return fmt.Errorf("board: store a password for %d: %w", userID, err)
		}
		if _, err := session.DeleteUser(ctx, tx, userID); err != nil {
			return err
		}
		return logMod(ctx, tx, Viewer{}, "user.reset_password", "user", userID, viaCLI, now)
	})
	if err != nil {
		return "", err
	}
	return pw, nil
}

// BanUser adds a user ban with no end date for member userID, ends every session
// of the member and writes one mod_log row ("user.ban", null actor,
// {"via":"cli"}), all in one transaction under FOR UPDATE on the user row.
// reason is for staff (the column reason); the banned member is not shown it.
//
// A member who does not exist is [ErrNotFound]. A member whom a user ban that has
// not ended holds already gets [ErrAlreadyBanned], and nothing is written. A
// reason that is not valid UTF-8, that holds a NUL, or that is over 255
// characters is an [*InputError] on the field "reason".
func BanUser(ctx context.Context, pool *pgxpool.Pool, userID int64, reason string, now time.Time) error {
	if !utf8.ValidString(reason) || strings.ContainsRune(reason, 0) {
		return inputError("reason", "The reason must be valid text.")
	}
	if utf8.RuneCountInString(reason) > maxBanReason {
		return inputError("reason", "The reason is too long.")
	}
	return inTx(ctx, pool, func(tx pgx.Tx) error {
		if err := lockUser(ctx, tx, userID); err != nil {
			return err
		}
		var banned bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from bans where kind = 'user' and user_id = $1 and (ends_at is null or ends_at > $2))`,
			userID, now).Scan(&banned); err != nil {
			return fmt.Errorf("board: check the bans of %d: %w", userID, err)
		}
		if banned {
			return ErrAlreadyBanned
		}
		if _, err := tx.Exec(ctx, `insert into bans (kind, user_id, reason, created_at) values ('user', $1, $2, $3)`, userID, reason, now); err != nil {
			return fmt.Errorf("board: ban %d: %w", userID, err)
		}
		if _, err := session.DeleteUser(ctx, tx, userID); err != nil {
			return err
		}
		return logMod(ctx, tx, Viewer{}, "user.ban", "user", userID, viaCLI, now)
	})
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
