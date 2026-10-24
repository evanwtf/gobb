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
// the own signature source, and which of the three forms the member may use.
type AccountPage struct {
	Email         string
	EmailVerified bool
	Signature     string

	CanPassword, CanEmail, CanSignature bool
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
	}
	err := q.QueryRow(ctx, `select email, email_verified, signature_source from users where id = $1 and state = 'active'`, v.UserID).
		Scan(&out.Email, &out.EmailVerified, &out.Signature)
	if errors.Is(err, pgx.ErrNoRows) {
		return AccountPage{}, ErrNotFound
	}
	if err != nil {
		return AccountPage{}, fmt.Errorf("board: read account of %d: %w", v.UserID, err)
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

// ChangePassword stores next as the password of member v. It needs
// account.password and applies [CheckNewPassword]. The caller has checked the
// current password (login.Service.Check); this function does not.
//
// The new hash is a gobb hash and password_scheme becomes "gobb". In the same
// transaction the function deletes every session of the member, so the old
// sessions end at the moment the new password starts. The caller then creates
// one new session for the browser that made the change. now is part of the
// signature of the board writes; a password change records no time.
func ChangePassword(ctx context.Context, pool *pgxpool.Pool, v Viewer, next string, _ time.Time) error {
	if err := mayAccount(v, "account.password"); err != nil {
		return err
	}
	if err := CheckNewPassword(ctx, pool, v, next); err != nil {
		return err
	}
	hash, err := password.Hash(next)
	if err != nil {
		// CheckNewPassword has refused every input that Hash refuses.
		return fmt.Errorf("board: hash a new password: %w", err)
	}
	return inTx(ctx, pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update users set password_hash = $2, password_scheme = 'gobb' where id = $1 and state = 'active'`, v.UserID, hash)
		if err != nil {
			return fmt.Errorf("board: store a password for %d: %w", v.UserID, err)
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		if _, err := session.DeleteUser(ctx, tx, v.UserID); err != nil {
			return err
		}
		return nil
	})
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

// ChangeEmail stores email as the address of member v. It needs account.email.
// The caller has checked the current password.
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
func ChangeEmail(ctx context.Context, pool *pgxpool.Pool, v Viewer, email string, _ time.Time) error {
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
		var current string
		err := tx.QueryRow(ctx, `select email_clean from users where id = $1 and state = 'active' for no key update`, v.UserID).Scan(&current)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("board: read the address of %d: %w", v.UserID, err)
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
