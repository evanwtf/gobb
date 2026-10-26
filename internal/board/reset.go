package board

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/account"
	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/internal/mail"
	"github.com/evanwtf/gobb/internal/session"
)

// The password reset by email. A link holds a token; the table password_resets
// holds only the SHA-256 of it. A link dies at its expiry, when the password
// epoch of the member moves, and when the member changes the email address.

const (
	// ResetLinkLife is how long a reset link works.
	ResetLinkLife = time.Hour
	// maxResetAccounts is the most accounts one request reaches. It is the most
	// accounts that one address can hold for a reset.
	maxResetAccounts = 5
	// maxResetName is the longest name that a request looks up, in bytes.
	maxResetName = 512
	// resetTokenLen is the length of a token: 32 bytes in base64url, no padding.
	resetTokenLen = 43
	// viaEmail is the details of the mod_log row that a completed reset writes.
	viaEmail = `{"via":"email"}`
)

// ResetMail builds the subject and the body of the reset email for the account
// username. token is the secret part of the link.
type ResetMail func(username, token string) (subject, body string)

// ResetAccount is the member that a live reset link belongs to.
type ResetAccount struct {
	UserID   int64
	Username string
}

// errResetSkip ends the transaction of one account that gets no email. It is
// not an error of the request.
var errResetSkip = errors.New("board: no reset email for this account")

// resetID returns the key of the link that has token: the SHA-256 in hex.
func resetID(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// validResetToken reports whether token has the form that [newResetToken]
// makes: 43 characters of base64url that decode to 32 bytes.
func validResetToken(token string) bool {
	if len(token) != resetTokenLen {
		return false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	return err == nil && len(raw) == 32
}

// newResetToken makes a token as the session package makes a session token: 32
// random bytes in base64url without padding.
func newResetToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("board: make a reset token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// RequestReset queues a reset email for each account that the name finds and
// that may get one. It returns how many emails it queued.
//
// The name is looked up as at sign-in: by username first, then by email address
// (every account that shares it, at most 5). An account gets an email only when
// all of these hold; otherwise the account is skipped and nothing shows:
//   - it is active;
//   - it has an address that [CleanEmailInput] accepts;
//   - no user ban and no email ban that has not ended holds it (an email ban
//     compares through [account.CleanEmail], as sign-in does; a ban on the
//     client's address does not count, because the member may ask from elsewhere);
//   - it has account.password (board-wide);
//   - allow, which is called with the user ID only for an account that passes
//     every rule above, returns true. allow may be nil.
//
// Each account has its own transaction: the user row FOR UPDATE, the rules again
// under the lock, one link (expires_at = now + [ResetLinkLife]) and one queued
// email whose send_by is the same expiry. A name that is empty after trimming,
// not valid UTF-8, holds a NUL or is over 512 bytes gives 0 and no error. An
// error names no account and no address, and holds no token.
func RequestReset(ctx context.Context, pool *pgxpool.Pool, name string, allow func(userID int64) bool, mailFor ResetMail, now time.Time) (queued int, err error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxResetName || !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
		return 0, nil
	}
	ids, err := findResetAccounts(ctx, pool, name)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	s, err := LoadSettings(ctx, pool)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		err := inTx(ctx, pool, func(tx pgx.Tx) error {
			return requestResetFor(ctx, tx, id, s, allow, mailFor, now)
		})
		if errors.Is(err, errResetSkip) {
			continue
		}
		if err != nil {
			return queued, err
		}
		queued++
	}
	return queued, nil
}

// findResetAccounts returns the IDs of the accounts that name finds: by username
// first, and only when none has it, by email address. It returns at most
// maxResetAccounts, in ID order.
func findResetAccounts(ctx context.Context, q db.Querier, name string) ([]int64, error) {
	ids, err := queryIDs(ctx, q, `select id from users where username_clean = $1 order by id limit $2`, account.CleanUsername(name))
	if err != nil || len(ids) > 0 {
		return ids, err
	}
	email := account.CleanEmail(name)
	if email == "" {
		return nil, nil
	}
	return queryIDs(ctx, q, `select id from users where email_clean = $1 order by id limit $2`, email)
}

func queryIDs(ctx context.Context, q db.Querier, sql, arg string) ([]int64, error) {
	rows, err := q.Query(ctx, sql, arg, maxResetAccounts)
	if err != nil {
		return nil, fmt.Errorf("board: find accounts for a reset: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("board: find accounts for a reset: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("board: find accounts for a reset: %w", err)
	}
	return ids, nil
}

// requestResetFor makes the link and queues the email for member id, or returns
// errResetSkip. It reads the user row FOR UPDATE first, so a ban or an address
// change that commits later waits, and one that committed earlier is seen.
func requestResetFor(ctx context.Context, tx pgx.Tx, id int64, s Settings, allow func(int64) bool, mailFor ResetMail, now time.Time) error {
	var (
		username, email, emailClean, state string
		epoch                              int64
	)
	err := tx.QueryRow(ctx, `
select username, email, email_clean, state, password_epoch from users where id = $1 for update`, id).
		Scan(&username, &email, &emailClean, &state, &epoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return errResetSkip
	}
	if err != nil {
		return fmt.Errorf("board: lock an account for a reset: %w", err)
	}
	if state != "active" {
		return errResetSkip
	}
	to, _, err := CleanEmailInput(email)
	if err != nil {
		return errResetSkip
	}
	held, err := resetBanned(ctx, tx, id, emailClean, now)
	if err != nil {
		return err
	}
	if held {
		return errResetSkip
	}
	v, err := Member(ctx, tx, id, s)
	if errors.Is(err, ErrNotFound) {
		return errResetSkip
	}
	if err != nil {
		return err
	}
	if mayAccount(v, "account.password") != nil {
		return errResetSkip
	}
	if allow != nil && !allow(id) {
		return errResetSkip
	}
	token, err := newResetToken()
	if err != nil {
		return err
	}
	expires := now.Add(ResetLinkLife)
	if _, err := tx.Exec(ctx, `
insert into password_resets (id, user_id, epoch, created_at, expires_at) values ($1, $2, $3, $4, $5)`,
		resetID(token), id, epoch, now, expires); err != nil {
		return fmt.Errorf("board: store a reset link: %w", err)
	}
	subject, body := mailFor(username, token)
	if _, err := mail.Enqueue(ctx, tx, mail.Message{To: to, Subject: subject, Body: body}, id, expires, now); err != nil {
		return fmt.Errorf("board: queue a reset email: %w", err)
	}
	return nil
}

// resetBanned reports whether a user ban, or an email ban that holds the address
// emailClean, has not ended at now. The email bans are compared in Go with
// [account.CleanEmail], as sign-in does. A blank address matches no ban.
func resetBanned(ctx context.Context, q db.Querier, userID int64, emailClean string, now time.Time) (bool, error) {
	rows, err := q.Query(ctx, `
select kind, email from bans
 where (ends_at is null or ends_at > $1)
   and ((kind = 'user' and user_id = $2) or kind = 'email')`, now, userID)
	if err != nil {
		return false, fmt.Errorf("board: read bans for a reset: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			kind  string
			email *string
		)
		if err := rows.Scan(&kind, &email); err != nil {
			return false, fmt.Errorf("board: read bans for a reset: %w", err)
		}
		if kind == "user" {
			return true, nil
		}
		if email != nil && emailClean != "" && account.CleanEmail(*email) == emailClean {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("board: read bans for a reset: %w", err)
	}
	return false, nil
}

// ResetLink returns the member that the link with token belongs to. It writes
// nothing. A token that is not 43 base64url characters, and a link that is
// unknown, at or past its expiry, of an older password epoch, of an account that
// is not active or lacks account.password (board-wide), or of a member whom a
// user ban holds, all give [ErrNotFound]: the page treats them alike.
func ResetLink(ctx context.Context, q db.Querier, token string, now time.Time) (ResetAccount, error) {
	acct, _, err := resetLink(ctx, q, token, now)
	return acct, err
}

// resetLink is [ResetLink] that also returns the viewer of the member.
func resetLink(ctx context.Context, q db.Querier, token string, now time.Time) (ResetAccount, Viewer, error) {
	if !validResetToken(token) {
		return ResetAccount{}, Viewer{}, ErrNotFound
	}
	var acct ResetAccount
	err := q.QueryRow(ctx, `
select u.id, u.username
  from password_resets r join users u on u.id = r.user_id
 where r.id = $1 and r.expires_at > $2 and r.epoch = u.password_epoch and u.state = 'active'`,
		resetID(token), now).Scan(&acct.UserID, &acct.Username)
	if errors.Is(err, pgx.ErrNoRows) {
		return ResetAccount{}, Viewer{}, ErrNotFound
	}
	if err != nil {
		return ResetAccount{}, Viewer{}, fmt.Errorf("board: read a reset link: %w", err)
	}
	s, err := LoadSettings(ctx, q)
	if err != nil {
		return ResetAccount{}, Viewer{}, err
	}
	v, err := Member(ctx, q, acct.UserID, s)
	if err != nil {
		return ResetAccount{}, Viewer{}, err
	}
	if mayAccount(v, "account.password") != nil {
		return ResetAccount{}, Viewer{}, ErrNotFound
	}
	if err := refuseBanned(ctx, q, acct.UserID, now); err != nil {
		if errors.Is(err, ErrBanned) {
			return ResetAccount{}, Viewer{}, ErrNotFound
		}
		return ResetAccount{}, Viewer{}, err
	}
	return acct, v, nil
}

// CompleteReset stores next as the password of the member that the link with
// token belongs to, and returns the member's ID. The link works once.
//
// It runs [ResetLink], then [CheckNewPassword] for that member (an
// [*InputError] on the field "next"; the link stays live), then the hash under
// hold, as [ChangePasswordWith] does. Then one transaction: the user row FOR
// UPDATE (the order of every write that ends sessions), the account still
// active, the link row FOR UPDATE, live and of the current password epoch, and
// no user ban. A failure of any of these is [ErrNotFound] and writes nothing:
// the password changed, a ban landed or another request used the link while the
// hash ran.
//
// The write stores the gobb hash, sets password_scheme to "gobb", adds one to
// the password epoch and sets email_verified (the link proved the address). It
// deletes every session and every link of the member and writes one mod_log row
// ("user.reset_password", the member as actor and target, {"via":"email"}). The
// token and the password are in no error and no row.
func CompleteReset(ctx context.Context, pool *pgxpool.Pool, token, next string, hold func(context.Context) (release func(), err error), now time.Time) (userID int64, err error) {
	acct, v, err := resetLink(ctx, pool, token, now)
	if err != nil {
		return 0, err
	}
	if err := CheckNewPassword(ctx, pool, v, next); err != nil {
		return 0, err
	}
	hash, err := hashHeld(ctx, next, hold)
	if err != nil {
		return 0, err
	}
	err = inTx(ctx, pool, func(tx pgx.Tx) error {
		if err := lockUser(ctx, tx, acct.UserID); err != nil {
			return err
		}
		var epoch int64
		err := tx.QueryRow(ctx, `select password_epoch from users where id = $1 and state = 'active'`, acct.UserID).Scan(&epoch)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("board: read the epoch of %d: %w", acct.UserID, err)
		}
		var linkEpoch int64
		err = tx.QueryRow(ctx, `
select epoch from password_resets where id = $1 and user_id = $2 and expires_at > $3 for update`,
			resetID(token), acct.UserID, now).Scan(&linkEpoch)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("board: lock a reset link: %w", err)
		}
		if linkEpoch != epoch {
			return ErrNotFound
		}
		if err := refuseBanned(ctx, tx, acct.UserID, now); err != nil {
			if errors.Is(err, ErrBanned) {
				return ErrNotFound
			}
			return err
		}
		if _, err := tx.Exec(ctx, `
update users set password_hash = $2, password_scheme = 'gobb', password_epoch = password_epoch + 1,
       email_verified = true
 where id = $1`, acct.UserID, hash); err != nil {
			return fmt.Errorf("board: store a password for %d: %w", acct.UserID, err)
		}
		if _, err := session.DeleteUser(ctx, tx, acct.UserID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from password_resets where user_id = $1`, acct.UserID); err != nil {
			return fmt.Errorf("board: delete the reset links of %d: %w", acct.UserID, err)
		}
		return logMod(ctx, tx, Viewer{UserID: acct.UserID}, "user.reset_password", "user", acct.UserID, viaEmail, now)
	})
	if err != nil {
		return 0, err
	}
	return acct.UserID, nil
}

// CleanupResets deletes the reset links at or past their expiry at now and
// returns how many it deleted. A link at exactly its expiry is dead.
func CleanupResets(ctx context.Context, q db.Querier, now time.Time) (int64, error) {
	tag, err := q.Exec(ctx, `delete from password_resets where expires_at <= $1`, now)
	if err != nil {
		return 0, fmt.Errorf("board: cleanup resets: %w", err)
	}
	return tag.RowsAffected(), nil
}
