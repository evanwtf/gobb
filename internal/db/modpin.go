package db

import (
	"context"
	"fmt"
)

// ModPinMigration is the migration that gives mod.pin wherever mod.lock is
// set. [DeriveModPin] runs the same file, so the importer and the migration
// share one copy of the SQL. An applied migration never changes, so the file is
// safe to run again.
const ModPinMigration = "0017_mod_pin.sql"

// DeriveModPin gives mod.pin wherever mod.lock is set, as migration
// [ModPinMigration] does, when no role setting and no grant names mod.pin. A
// bundle that names mod.pin anywhere keeps its own settings, and nothing is
// derived. The importer calls it after it loads the roles and the grants,
// because the migrations ran on an empty database. derived reports whether the
// copy ran.
func DeriveModPin(ctx context.Context, q Querier) (derived bool, err error) {
	var named bool
	if err := q.QueryRow(ctx, `
select exists (select 1 from acl_role_settings where option = 'mod.pin')
    or exists (select 1 from acl_group_grants where option = 'mod.pin')
    or exists (select 1 from acl_user_grants where option = 'mod.pin')`).Scan(&named); err != nil {
		return false, fmt.Errorf("db: look for mod.pin: %w", err)
	}
	if named {
		return false, nil
	}
	sql, err := embedded.ReadFile("migrations/" + ModPinMigration)
	if err != nil {
		return false, fmt.Errorf("db: read %s: %w", ModPinMigration, err)
	}
	// No arguments: the statements of the file run in one simple-protocol call.
	if _, err := q.Exec(ctx, string(sql)); err != nil {
		return false, fmt.Errorf("db: derive mod.pin: %w", err)
	}
	return true, nil
}
