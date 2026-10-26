package board

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/text/unicode/norm"

	"github.com/evanwtf/gobb/internal/account"
	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/internal/password"
	"github.com/evanwtf/gobb/internal/session"
	"github.com/evanwtf/gobb/markup"
)

// The limits of the account forms.
const (
	minPasswordChars = 10
	maxPasswordBytes = 1024
	maxEmailBytes    = 254
)

// The messages of the account forms. Each one names the problem, and none holds
// text from the database or from the request.
const (
	msgPasswordShort = "Use at least 10 characters."
	msgPasswordLong  = "Use at most 1,024 bytes."
	msgPasswordName  = "Choose a password that differs from your username."
	msgPasswordText  = "Use a password that is valid UTF-8."
	msgEmailTaken    = "That address belongs to another account."
	msgEmailForm     = "Enter a valid email address."
)

// AccountPage is what the account page shows to a member: the own email address,
// the own signature source, the own settings, and which of the forms the member
// may use.
type AccountPage struct {
	Email         string
	EmailVerified bool
	Signature     string

	// Timezone is the stored zone name. It can be a name that gobb no longer
	// lists in [tz.Zones]. DateFormat is "standard", "relative" or "iso".
	Timezone, DateFormat string
	// The "About you" fields, as stored. A birthday part that is unset is 0.
	Location, Occupation, Interests, Website string
	BirthDay, BirthMonth, BirthYear          int
	// AvatarPath is the stored avatar path, or empty.
	AvatarPath string

	CanPassword, CanEmail, CanSignature, CanProfile, CanAvatar bool
}

// mayAccount returns [ErrForbidden] for a guest and for a member who lacks
// option (board-wide). Every account write calls it first.
func mayAccount(v Viewer, option string) error {
	if v.UserID == 0 || !v.Perms.Has(option, 0) {
		return ErrForbidden
	}
	return nil
}

// Account returns the account page of member v. A guest gets [ErrForbidden]. The
// page holds the own email address and signature source of the member and no
// other member's data. It does not check the rights to change them: the page
// shows the forms that the rights allow, and each write checks again.
func Account(ctx context.Context, q db.Querier, v Viewer) (AccountPage, error) {
	if v.UserID == 0 {
		return AccountPage{}, ErrForbidden
	}
	out := AccountPage{
		CanPassword:  v.Perms.Has("account.password", 0),
		CanEmail:     v.Perms.Has("account.email", 0),
		CanSignature: v.Perms.Has("account.signature", 0),
		CanProfile:   v.Perms.Has("account.profile", 0),
		CanAvatar:    v.Perms.Has("account.avatar", 0),
	}
	var day, month, year *int16
	var avatar *string
	err := q.QueryRow(ctx, `
select email, email_verified, signature_source, timezone, date_format,
       location, occupation, interests, website, birth_day, birth_month, birth_year, avatar_path
  from users where id = $1 and state = 'active'`, v.UserID).
		Scan(&out.Email, &out.EmailVerified, &out.Signature, &out.Timezone, &out.DateFormat,
			&out.Location, &out.Occupation, &out.Interests, &out.Website, &day, &month, &year, &avatar)
	if errors.Is(err, pgx.ErrNoRows) {
		return AccountPage{}, ErrNotFound
	}
	if err != nil {
		return AccountPage{}, fmt.Errorf("board: read account of %d: %w", v.UserID, err)
	}
	if day != nil {
		out.BirthDay = int(*day)
	}
	if month != nil {
		out.BirthMonth = int(*month)
	}
	if year != nil {
		out.BirthYear = int(*year)
	}
	if avatar != nil {
		out.AvatarPath = *avatar
	}
	return out, nil
}

// CheckNewPassword applies the rules for a new password to next, for member v,
// and changes nothing. It runs no hash, so a page may call it before the
// costly check of the current password.
//
//   - The length counts characters (in NFC form, the form that is hashed) for
//     the minimum of 10, and bytes for the maximum of 1,024.
//   - The password must be valid UTF-8.
//   - The password may not equal the username after [account.CleanUsername].
//
// A refusal is an [*InputError] on the field "next". It never holds the password.
func CheckNewPassword(ctx context.Context, q db.Querier, v Viewer, next string) error {
	var usernameClean string
	err := q.QueryRow(ctx, `select username_clean from users where id = $1 and state = 'active'`, v.UserID).Scan(&usernameClean)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("board: read username of %d: %w", v.UserID, err)
	}
	switch {
	case !utf8.ValidString(next):
		return inputError("next", msgPasswordText)
	case len(next) > maxPasswordBytes:
		return inputError("next", msgPasswordLong)
	case utf8.RuneCountInString(norm.NFC.String(next)) < minPasswordChars:
		return inputError("next", msgPasswordShort)
	case account.CleanUsername(next) == usernameClean:
		return inputError("next", msgPasswordName)
	}
	return nil
}

// ErrPasswordChanged means that a change which needs the current password found
// the password epoch of the member higher than the one that the check of the
// current password read: the password changed in between (another browser, an
// administrator's reset). The write is refused and changes nothing. The web layer
// answers it like a wrong current password.
var ErrPasswordChanged = errors.New("board: the password changed since it was checked")

// ChangePasswordWith stores next as the password of member v, for a change that
// the member asked for after a check of the current password. It needs
// account.password and applies [CheckNewPassword].
//
// The new hash is a gobb hash and password_scheme becomes "gobb". In the same
// transaction the function adds one to the password epoch and deletes every
// session of the member, so the old sessions end at the moment the new password
// starts. The time is part of the signature of the board writes; a password
// change records no time.
//
// epoch is the password epoch that the check of the current password read
// ([login.Service.CheckWith]); under the row lock, the function confirms that
// the row still holds it. If not, it returns [ErrPasswordChanged] and writes
// nothing. Under the same lock it refuses a member whom a user ban holds
// ([ErrBanned]) and writes nothing: a change that was in flight when the ban
// landed does not survive it. It returns the epoch it stored, for the caller
// that starts a session with [session.CreateChecked]. It takes hold, when not
// nil, around the hash and nowhere else: the argon2 run needs memory, and the
// caller's gate bounds it. An error from hold is returned as it is, before
// anything is written. The release runs before the transaction opens, so the
// gate share is not held across the database work.
//
// The transaction reads the user row FOR UPDATE before it writes or deletes. A
// sign-in that verified the old password and is about to insert a session holds
// or wants the row FOR SHARE (session.CreateChecked), so each such pair runs in
// one order: the sign-in's session is in the table before the delete, or the
// sign-in sees the new epoch and is refused. Any write that ends the sessions of
// a member (a ban, a reset by an administrator) takes the same lock first, and a
// reset adds one to the epoch, as a change does.
func ChangePasswordWith(ctx context.Context, pool *pgxpool.Pool, v Viewer, next string, epoch int64, hold func(context.Context) (release func(), err error), now time.Time) (int64, error) {
	return changePassword(ctx, pool, v, next, &epoch, hold, now)
}

func changePassword(ctx context.Context, pool *pgxpool.Pool, v Viewer, next string, want *int64, hold func(context.Context) (release func(), err error), now time.Time) (int64, error) {
	if err := mayAccount(v, "account.password"); err != nil {
		return 0, err
	}
	if err := CheckNewPassword(ctx, pool, v, next); err != nil {
		return 0, err
	}
	hash, err := hashHeld(ctx, next, hold)
	if err != nil {
		return 0, err
	}
	var stored int64
	err = inTx(ctx, pool, func(tx pgx.Tx) error {
		var epoch int64
		err := tx.QueryRow(ctx, `select password_epoch from users where id = $1 and state = 'active' for update`, v.UserID).Scan(&epoch)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("board: lock user %d: %w", v.UserID, err)
		}
		if err := refuseBanned(ctx, tx, v.UserID, now); err != nil {
			return err
		}
		if want != nil && epoch != *want {
			return ErrPasswordChanged
		}
		err = tx.QueryRow(ctx, `
update users set password_hash = $2, password_scheme = 'gobb', password_epoch = password_epoch + 1
 where id = $1 returning password_epoch`, v.UserID, hash).Scan(&stored)
		if err != nil {
			return fmt.Errorf("board: store a password for %d: %w", v.UserID, err)
		}
		if _, err := session.DeleteUser(ctx, tx, v.UserID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return stored, nil
}

// hashHeld hashes next with the gate share of hold, when there is one.
func hashHeld(ctx context.Context, next string, hold func(context.Context) (func(), error)) (string, error) {
	if hold != nil {
		release, err := hold(ctx)
		if err != nil {
			return "", err
		}
		defer release()
	}
	hash, err := password.Hash(next)
	if err != nil {
		// CheckNewPassword has refused every input that Hash refuses.
		return "", fmt.Errorf("board: hash a new password: %w", err)
	}
	return hash, nil
}

// CleanEmailInput returns the address as stored (trimmed) and its comparison
// form, or an [*InputError] on the field "email". The address must be one plain
// address of at most 254 bytes: valid UTF-8, no control character, no space, no
// display name, and a local part and a domain.
func CleanEmailInput(email string) (stored, clean string, err error) {
	stored = strings.TrimSpace(email)
	bad := stored == "" || len(stored) > maxEmailBytes || !utf8.ValidString(stored) ||
		strings.IndexFunc(stored, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0
	if !bad {
		addr, perr := mail.ParseAddress(stored)
		bad = perr != nil || addr.Address != stored || addr.Name != "" || strings.Count(stored, "@") != 1 ||
			strings.HasPrefix(stored, "@") || strings.HasSuffix(stored, "@")
	}
	if bad {
		return "", "", inputError("email", msgEmailForm)
	}
	return stored, account.CleanEmail(stored), nil
}

// ChangeEmailWith stores email as the address of member v, for a change that the
// member asked for after a check of the current password. It needs
// account.email.
//
//   - An address that another account holds, compared with
//     [account.CleanEmail] and a different user ID, is an [*InputError] on the
//     field "email". Two members who share an address today are not touched.
//   - The member's own address, unchanged, is a no-op: no write, and
//     email_verified stays.
//   - A new address sets email_verified to false.
//
// The check and the write run under a lock on the address, so two members who
// ask for one free address at once cannot both get it.
//
// epoch is the password epoch that the check read. Under the lock on the user
// row, before anything else, the function confirms that the row still holds it;
// if not, it returns [ErrPasswordChanged] and writes nothing. Under the same lock
// it refuses a member whom a user ban holds ([ErrBanned]) and writes nothing. The
// row lock (FOR NO KEY UPDATE) conflicts with the FOR UPDATE of a password
// change, so the epoch it reads is the committed one.
func ChangeEmailWith(ctx context.Context, pool *pgxpool.Pool, v Viewer, email string, epoch int64, now time.Time) error {
	return changeEmail(ctx, pool, v, email, &epoch, now)
}

func changeEmail(ctx context.Context, pool *pgxpool.Pool, v Viewer, email string, want *int64, now time.Time) error {
	if err := mayAccount(v, "account.email"); err != nil {
		return err
	}
	stored, clean, err := CleanEmailInput(email)
	if err != nil {
		return err
	}
	return inTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, "gobb:email:"+clean); err != nil {
			return fmt.Errorf("board: lock an email address: %w", err)
		}
		var (
			current string
			epoch   int64
		)
		err := tx.QueryRow(ctx, `select email_clean, password_epoch from users where id = $1 and state = 'active' for no key update`, v.UserID).Scan(&current, &epoch)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("board: read the address of %d: %w", v.UserID, err)
		}
		if err := refuseBanned(ctx, tx, v.UserID, now); err != nil {
			return err
		}
		if want != nil && epoch != *want {
			return ErrPasswordChanged
		}
		if current == clean {
			return nil
		}
		var taken bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from users where email_clean = $1 and id <> $2)`, clean, v.UserID).Scan(&taken); err != nil {
			return fmt.Errorf("board: check an address: %w", err)
		}
		if taken {
			return inputError("email", msgEmailTaken)
		}
		if _, err := tx.Exec(ctx, `update users set email = $2, email_clean = $3, email_verified = false where id = $1`, v.UserID, stored, clean); err != nil {
			return fmt.Errorf("board: store an address for %d: %w", v.UserID, err)
		}
		// A reset link goes to the old address, so it dies with it.
		if _, err := tx.Exec(ctx, `delete from password_resets where user_id = $1`, v.UserID); err != nil {
			return fmt.Errorf("board: delete the reset links of %d: %w", v.UserID, err)
		}
		return nil
	})
}

// SetSignature stores src as the signature of member v. It needs
// account.signature. The text goes through [ParseBody] with forum 0 and the
// stored signature flags of the member, so the limits (2,000 bytes), the checks
// of the tree and the rule for images are those of every other member text. A
// text that is empty or only white space clears the signature. A failure is an
// [*InputError] on the field "body".
func SetSignature(ctx context.Context, pool *pgxpool.Pool, v Viewer, src string, _ time.Time) error {
	if err := mayAccount(v, "account.signature"); err != nil {
		return err
	}
	return inTx(ctx, pool, func(tx pgx.Tx) error {
		var flags markup.Options
		err := tx.QueryRow(ctx, `
select signature_markdown, signature_smilies, signature_autolink
  from users where id = $1 and state = 'active' for no key update`, v.UserID).
			Scan(&flags.Markdown, &flags.Smilies, &flags.Autolink)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("board: read the signature flags of %d: %w", v.UserID, err)
		}
		body, err := ParseBody(ctx, tx, v, 0, src, &flags)
		if err != nil {
			return err
		}
		var tree any
		if strings.TrimSpace(src) == "" {
			src = ""
		} else {
			tree = string(body.JSON)
		}
		if _, err := tx.Exec(ctx, `update users set signature_source = $2, signature_tree = $3::jsonb where id = $1`, v.UserID, src, tree); err != nil {
			return fmt.Errorf("board: store a signature for %d: %w", v.UserID, err)
		}
		return nil
	})
}

// refuseBanned gives [ErrBanned] when a user ban that has not ended holds member
// userID at now. The caller holds the lock on the user row, so a ban that commits
// later waits for the write, and a ban that committed earlier is seen.
func refuseBanned(ctx context.Context, q db.Querier, userID int64, now time.Time) error {
	var banned bool
	err := q.QueryRow(ctx, `select exists (select 1 from bans where kind = 'user' and user_id = $1 and (ends_at is null or ends_at > $2))`,
		userID, now).Scan(&banned)
	if err != nil {
		return fmt.Errorf("board: read bans of user %d: %w", userID, err)
	}
	if banned {
		return ErrBanned
	}
	return nil
}
