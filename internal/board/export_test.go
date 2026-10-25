package board

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ChangePassword stores next as the password of member v, with no check of a
// current password and no epoch. Production code changes a password through
// ChangePasswordWith only, after a check; the tests of this package use this.
func ChangePassword(ctx context.Context, pool *pgxpool.Pool, v Viewer, next string, now time.Time) error {
	_, err := changePassword(ctx, pool, v, next, nil, nil, now)
	return err
}

// ChangeEmail stores email as the address of member v, with no check of a current
// password and no epoch. Production code uses ChangeEmailWith only.
func ChangeEmail(ctx context.Context, pool *pgxpool.Pool, v Viewer, email string, now time.Time) error {
	return changeEmail(ctx, pool, v, email, nil, now)
}

// SetParseHook sets the hook that ParseBody runs just before the parse, and
// returns the function that puts the old one back. A test uses it to hold a parse
// open and to see what the database holds meanwhile.
func SetParseHook(f func()) (restore func()) {
	old := parseHook
	parseHook = f
	return func() { parseHook = old }
}
