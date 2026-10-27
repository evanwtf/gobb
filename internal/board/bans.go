package board

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/account"
	"github.com/evanwtf/gobb/internal/db"
)

// The bans of the admin pages: the list of the bans in force, the add and the
// lift. A ban is one row of bans. Sign-in reads all three kinds, guest posting
// reads "ip" and the reset request reads "email", so a ban stores its target in
// the form that those readers compare:
//
//   - "user": the ID of the member (user_id).
//   - "email": the clean form of the address ([account.CleanEmail]).
//   - "ip": an inet range in cidr form with no host bits set. One address is a
//     range of one: /32 for IPv4, /128 for IPv6.

// banPerPage is the count of bans on one page of the list.
const banPerPage = 50

// The widest range that a ban takes without a second confirm.
const (
	wideBitsV4 = 16
	wideBitsV6 = 48
)

// The kinds of ban.
const (
	BanUserKind  = "user"
	BanEmailKind = "email"
	BanIPKind    = "ip"
)

// BanDraft is what an admin sends to add a ban.
type BanDraft struct {
	// Kind is "user", "email" or "ip".
	Kind string
	// Target is a username, an email address, or an IP address or range in CIDR
	// form, as the kind says.
	Target string
	// Reason is for staff. ReasonShown is the text that the banned member reads
	// at sign-in. Both are optional.
	Reason, ReasonShown string
	// EndsAt is the end of the ban, or nil for a ban with no end. It must be after
	// the time of the add.
	EndsAt *time.Time
	// ConfirmWide is the second confirm that a range wider than /16 (IPv4) or /48
	// (IPv6) needs.
	ConfirmWide bool
	// AdminIP is the address that the admin writes from, or the zero Addr when it
	// is not known. An "ip" ban that holds it is refused.
	AdminIP netip.Addr
}

// BanEntry is one ban in force, with what the Bans page shows.
type BanEntry struct {
	ID   int64
	Kind string
	// Member is the member of a "user" ban. Its Name is the username now.
	Member Author
	// Email is the address of an "email" ban, as stored.
	Email string
	// Range is the range of an "ip" ban: a bare address for a range of one.
	Range               string
	Reason, ReasonShown string
	CreatedAt           time.Time
	EndsAt              *time.Time
	// CreatedBy is the admin who added the ban. Its ID is 0 for a ban from the
	// command line or the import, and for an admin who is gone.
	CreatedBy Author
}

// Target returns the text of the target of the entry: the username, the address
// or the range.
func (e BanEntry) Target() string {
	switch e.Kind {
	case BanUserKind:
		return e.Member.Name
	case BanEmailKind:
		return e.Email
	}
	return e.Range
}

// BanPage is one page of the bans in force, newest first.
type BanPage struct {
	Entries     []BanEntry
	Page, Pages int
}

// ListBans returns page page of the bans that have not ended at now, newest
// first, 50 a page. A page past the last is [ErrNotFound]; page 1 of an empty
// list is not. It does not check the viewer: the caller is the admin area, behind
// its gate.
func ListBans(ctx context.Context, q db.Querier, page int, now time.Time) (BanPage, error) {
	var n int
	if err := q.QueryRow(ctx, `select count(*) from bans where ends_at is null or ends_at > $1`, now).Scan(&n); err != nil {
		return BanPage{}, fmt.Errorf("board: count the bans: %w", err)
	}
	out := BanPage{Page: page, Pages: pageCount(n, banPerPage)}
	if page < 1 || page > out.Pages {
		return BanPage{}, ErrNotFound
	}
	rows, err := q.Query(ctx, `
select b.id, b.kind, coalesce(b.user_id, 0), coalesce(u.username, ''), coalesce(b.email, ''), coalesce(b.ip::text, ''),
       b.reason, b.reason_shown, b.created_at, b.ends_at, coalesce(b.created_by, 0), coalesce(c.username, '')
  from bans b
  left join users u on u.id = b.user_id
  left join users c on c.id = b.created_by
 where b.ends_at is null or b.ends_at > $1
 order by b.created_at desc, b.id desc
 limit $2 offset $3`, now, banPerPage, (page-1)*banPerPage)
	if err != nil {
		return BanPage{}, fmt.Errorf("board: read the bans: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			e    BanEntry
			cidr string
		)
		if err := rows.Scan(&e.ID, &e.Kind, &e.Member.ID, &e.Member.Name, &e.Email, &cidr, &e.Reason, &e.ReasonShown,
			&e.CreatedAt, &e.EndsAt, &e.CreatedBy.ID, &e.CreatedBy.Name); err != nil {
			return BanPage{}, fmt.Errorf("board: read the bans: %w", err)
		}
		e.Range = rangeText(cidr)
		out.Entries = append(out.Entries, e)
	}
	if err := rows.Err(); err != nil {
		return BanPage{}, fmt.Errorf("board: read the bans: %w", err)
	}
	return out, nil
}

// rangeText returns the range cidr as text: a bare address for a range of one,
// the CIDR form otherwise, and empty for text that is not a range.
func rangeText(cidr string) string {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return ""
	}
	if p.Bits() == p.Addr().BitLen() {
		return p.Addr().String()
	}
	return p.String()
}

// ParseBanRange reads target, an IP address or a range in CIDR form, as the
// range that a ban stores: with no host bits set and no zone. One address is a
// range of one. An IPv4-mapped IPv6 address or range is an [*InputError] on the
// field "target": sign-in compares the plain IPv4 form. So is anything that is
// neither an address nor a range.
func ParseBanRange(target string) (netip.Prefix, error) {
	text := strings.TrimSpace(target)
	bad := inputError("target", "Enter an IP address, or a range such as 192.0.2.0/24 or 2001:db8::/32.")
	var p netip.Prefix
	if strings.Contains(text, "/") {
		var err error
		if p, err = netip.ParsePrefix(text); err != nil {
			return netip.Prefix{}, bad
		}
	} else {
		a, err := netip.ParseAddr(text)
		if err != nil || a.Zone() != "" {
			return netip.Prefix{}, bad
		}
		p = netip.PrefixFrom(a, a.BitLen())
	}
	// ParsePrefix refuses a zone, and PrefixFrom drops one, so the zone is
	// checked above, on the address.
	if !p.IsValid() {
		return netip.Prefix{}, bad
	}
	if p.Addr().Is4In6() {
		return netip.Prefix{}, inputError("target", "Use the IPv4 form of the address, such as 192.0.2.1.")
	}
	return p.Masked(), nil
}

// isWide reports whether p is wider than /16 (IPv4) or /48 (IPv6).
func isWide(p netip.Prefix) bool {
	if p.Addr().Is4() {
		return p.Bits() < wideBitsV4
	}
	return p.Bits() < wideBitsV6
}

// AddBan adds the ban d for the admin v and returns its ID. The ban and its
// mod_log row ("ban.add", target type "ban", the ID of the ban, details with the
// kind, the target and the end) are written in one transaction ([AdminWrite]).
// A viewer without the board-wide admin right gets [ErrForbidden].
//
// A "user" ban runs the write of [BanUser]: the member's sessions end and the
// addresses that the member used are removed.
//
// A refusal is an [*InputError] on the field that is wrong:
//   - "kind" is not one of the three.
//   - "target" is empty or malformed; names no member; names the admin, an owner
//     (a user ban) or the address of an owner or of the admin (an email ban); is a
//     range that holds the address of the admin; or is held by a ban that has not
//     ended.
//   - "confirm_wide" is missing for a range wider than /16 or /48.
//   - "reason", "reason_shown" is not valid text or is over 255 characters.
//   - "ends" is not after now.
func AddBan(ctx context.Context, pool *pgxpool.Pool, v Viewer, d BanDraft, now time.Time) (int64, error) {
	if v.UserID == 0 || !v.Perms.IsAdmin() {
		return 0, ErrForbidden
	}
	d.Reason, d.ReasonShown = strings.TrimSpace(d.Reason), strings.TrimSpace(d.ReasonShown)
	if err := checkBanText("reason", d.Reason); err != nil {
		return 0, err
	}
	if err := checkBanText("reason_shown", d.ReasonShown); err != nil {
		return 0, err
	}
	if d.EndsAt != nil && !d.EndsAt.After(now) {
		return 0, inputError("ends", "The end must be in the future.")
	}
	var (
		id      int64
		prepare func(tx pgx.Tx) (Change, error)
	)
	switch d.Kind {
	case BanUserKind:
		prepare = func(tx pgx.Tx) (Change, error) { return addUserBan(ctx, tx, v, d, now, &id) }
	case BanEmailKind:
		_, clean, err := CleanEmailInput(d.Target)
		if err != nil {
			return 0, inputError("target", msgEmailForm)
		}
		prepare = func(tx pgx.Tx) (Change, error) { return addEmailBan(ctx, tx, v, d, clean, now, &id) }
	case BanIPKind:
		p, err := ParseBanRange(d.Target)
		if err != nil {
			return 0, err
		}
		if isWide(p) && !d.ConfirmWide {
			return 0, inputError("confirm_wide", "Confirm the wide range, or enter a smaller one.")
		}
		if d.AdminIP.IsValid() && p.Contains(d.AdminIP.Unmap().WithZone("")) {
			return 0, inputError("target", "This range holds your own address. You would lock yourself out.")
		}
		prepare = func(tx pgx.Tx) (Change, error) { return addRangeBan(ctx, tx, v, d, p, now, &id) }
	default:
		return 0, inputError("kind", "Pick a kind of ban.")
	}
	if err := AdminWrite(ctx, pool, v, now, prepare); err != nil {
		return 0, err
	}
	return id, nil
}

// banChange returns the [Change] of a ban that was added. target is the text of
// the target: the username, the clean address or the range.
func banChange(action string, id int64, kind, target string, endsAt *time.Time) Change {
	details := map[string]any{"kind": kind, "target": target}
	if endsAt != nil {
		details["ends"] = endsAt.UTC().Format(time.RFC3339)
	}
	return Change{Action: action, TargetType: "ban", TargetID: id, Details: details}
}

// addUserBan is the write of a "user" ban. It finds the member by the clean
// username, as sign-in does.
func addUserBan(ctx context.Context, tx pgx.Tx, v Viewer, d BanDraft, now time.Time, id *int64) (Change, error) {
	userID, username, err := UserByName(ctx, tx, d.Target)
	switch {
	case errors.Is(err, ErrNotFound):
		return Change{}, inputError("target", "No member has that name.")
	case err != nil:
		return Change{}, err
	case userID == v.UserID:
		return Change{}, inputError("target", "You cannot ban yourself.")
	}
	*id, err = banUserTx(ctx, tx, userBan{userID: userID, reason: d.Reason, shown: d.ReasonShown,
		endsAt: d.EndsAt, createdBy: v.UserID, refuseOwner: true}, now)
	switch {
	case errors.Is(err, ErrAlreadyBanned):
		return Change{}, inputError("target", "That member is already banned.")
	case errors.Is(err, ErrNotFound):
		return Change{}, inputError("target", "No member has that name.")
	case err != nil:
		return Change{}, err
	}
	return banChange("ban.add", *id, BanUserKind, username, d.EndsAt), nil
}

// addEmailBan is the write of an "email" ban on the clean address clean. It
// refuses the address of the admin and the address of an owner, which would shut
// them out of sign-in, and an address that a ban in force holds already. The
// bans in force are compared in Go with [account.CleanEmail], as sign-in does.
func addEmailBan(ctx context.Context, tx pgx.Tx, v Viewer, d BanDraft, clean string, now time.Time, id *int64) (Change, error) {
	var mine, owner bool
	if err := tx.QueryRow(ctx, `
select coalesce(bool_or(id = $2), false), coalesce(bool_or(is_owner), false) from users where email_clean = $1`,
		clean, v.UserID).Scan(&mine, &owner); err != nil {
		return Change{}, fmt.Errorf("board: check the owners for a ban: %w", err)
	}
	switch {
	case mine:
		return Change{}, inputError("target", "That is your own address. You cannot ban yourself.")
	case owner:
		return Change{}, inputError("target", "That is the address of an owner. An owner cannot be banned.")
	}
	rows, err := tx.Query(ctx, `select email from bans where kind = 'email' and email is not null and (ends_at is null or ends_at > $1)`, now)
	if err != nil {
		return Change{}, fmt.Errorf("board: read the email bans: %w", err)
	}
	held, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return Change{}, fmt.Errorf("board: read the email bans: %w", err)
	}
	for _, e := range held {
		if account.CleanEmail(e) == clean {
			return Change{}, inputError("target", "A ban holds that address already.")
		}
	}
	if err := tx.QueryRow(ctx, `
insert into bans (kind, email, reason, reason_shown, created_at, ends_at, created_by)
values ('email', $1, $2, $3, $4, $5, $6) returning id`,
		clean, d.Reason, d.ReasonShown, now, d.EndsAt, v.UserID).Scan(id); err != nil {
		return Change{}, fmt.Errorf("board: ban an address: %w", err)
	}
	return banChange("ban.add", *id, BanEmailKind, clean, d.EndsAt), nil
}

// addRangeBan is the write of an "ip" ban on the range p. It refuses a range that
// a ban in force holds with the same bounds.
func addRangeBan(ctx context.Context, tx pgx.Tx, v Viewer, d BanDraft, p netip.Prefix, now time.Time, id *int64) (Change, error) {
	var held bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from bans where kind = 'ip' and ip = $1::cidr and (ends_at is null or ends_at > $2))`,
		p.String(), now).Scan(&held); err != nil {
		return Change{}, fmt.Errorf("board: read the range bans: %w", err)
	}
	if held {
		return Change{}, inputError("target", "A ban holds that range already.")
	}
	if err := tx.QueryRow(ctx, `
insert into bans (kind, ip, reason, reason_shown, created_at, ends_at, created_by)
values ('ip', $1::cidr, $2, $3, $4, $5, $6) returning id`,
		p.String(), d.Reason, d.ReasonShown, now, d.EndsAt, v.UserID).Scan(id); err != nil {
		return Change{}, fmt.Errorf("board: ban a range: %w", err)
	}
	return banChange("ban.add", *id, BanIPKind, rangeText(p.String()), d.EndsAt), nil
}

// LiftBan removes ban banID, as [UnbanUser] removes the user bans of a member,
// and writes one mod_log row ("ban.lift", target type "ban", the ID of the ban,
// details with the kind and the target), in one transaction ([AdminWrite]). A
// ban of a member takes the user row FOR UPDATE first, so a sign-in in flight is
// ordered against the lift as it is against [BanUser]. A viewer without the
// board-wide admin right gets [ErrForbidden]. A ban that does not exist, or has
// ended, is [ErrNotFound], and nothing is written.
func LiftBan(ctx context.Context, pool *pgxpool.Pool, v Viewer, banID int64, now time.Time) error {
	return AdminWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		var userID int64
		err := tx.QueryRow(ctx, `select coalesce(user_id, 0) from bans where id = $1`, banID).Scan(&userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return Change{}, ErrNotFound
		}
		if err != nil {
			return Change{}, fmt.Errorf("board: read ban %d: %w", banID, err)
		}
		if userID != 0 {
			if err := lockUser(ctx, tx, userID); err != nil {
				return Change{}, err
			}
		}
		var (
			kind, username, email, cidr string
		)
		err = tx.QueryRow(ctx, `
with gone as (
  delete from bans where id = $1 and (ends_at is null or ends_at > $2)
  returning kind, user_id, email, ip)
select g.kind, coalesce(u.username, ''), coalesce(g.email, ''), coalesce(g.ip::text, '')
  from gone g left join users u on u.id = g.user_id`, banID, now).Scan(&kind, &username, &email, &cidr)
		if errors.Is(err, pgx.ErrNoRows) {
			return Change{}, ErrNotFound
		}
		if err != nil {
			return Change{}, fmt.Errorf("board: lift ban %d: %w", banID, err)
		}
		target := username
		switch kind {
		case BanEmailKind:
			target = email
		case BanIPKind:
			target = rangeText(cidr)
		}
		return banChange("ban.lift", banID, kind, target, nil), nil
	})
}
