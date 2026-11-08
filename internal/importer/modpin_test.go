package importer

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/dbtest"
)

// optionRows lists every setting of option as "role R:S", "group G/F:S" or
// "user U/F:S", sorted.
func optionRows(t *testing.T, pool *pgxpool.Pool, option string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
select 'role ' || role_id || ':' || setting from acl_role_settings where option = $1
union all
select 'group ' || group_id || '/' || forum_id || ':' || setting from acl_group_grants where option = $1
union all
select 'user ' || user_id || '/' || forum_id || ':' || setting from acl_user_grants where option = $1
order by 1`, option)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// grantOpt is one option grant of a group (or of a user, when user is set).
func grantOpt(group, user, forum int64, option string, s bundle.Setting) bundle.Grant {
	g := bundle.Grant{ForumID: forum, Option: &option, Setting: &s}
	if user != 0 {
		g.UserID = &user
	} else {
		g.GroupID = &group
	}
	return g
}

// TestImportDerivesModPin: a bundle that names no mod.pin gets a mod.pin setting
// for each mod.lock setting, with the same holder, forum and value. The plain
// import and the --replace import both derive it.
func TestImportDerivesModPin(t *testing.T) {
	src := writeBoard(t, func(b *bundletest.Board) {
		// The small board sets no mod.lock. Add one group grant, one Never for
		// a user, and a moderator role that holds it.
		b.Grants = append(b.Grants,
			grantOpt(3, 0, 2, "mod.lock", bundle.Yes),
			grantOpt(0, 3, 3, "mod.lock", bundle.Never))
		b.Roles = append(b.Roles, bundle.Role{ID: 3, Name: "Lockers", Kind: "mod", Position: 3,
			Settings: []bundle.RoleSetting{{Option: "mod.lock", Setting: bundle.Yes}}})
	})
	want := []string{"group 3/2:3", "role 3:3", "user 3/3:2"}

	pool := dbtest.New(t)
	mustRun(t, pool, src, t.TempDir(), false)
	if got := optionRows(t, pool, "mod.lock"); !slices.Equal(got, want) {
		t.Fatalf("mod.lock rows %v, want %v", got, want)
	}
	if got := optionRows(t, pool, "mod.pin"); !slices.Equal(got, want) {
		t.Errorf("mod.pin rows %v, want %v", got, want)
	}

	// --replace empties the tables and derives again, with no duplicates.
	mustRun(t, pool, src, t.TempDir(), true)
	if got := optionRows(t, pool, "mod.pin"); !slices.Equal(got, want) {
		t.Errorf("after --replace, mod.pin rows %v, want %v", got, want)
	}
}

// TestImportKeepsBundleModPin: a bundle that names mod.pin anywhere keeps full
// control. Nothing is derived from its mod.lock settings.
func TestImportKeepsBundleModPin(t *testing.T) {
	src := writeBoard(t, func(b *bundletest.Board) {
		b.Grants = append(b.Grants,
			grantOpt(3, 0, 2, "mod.lock", bundle.Yes),
			grantOpt(3, 0, 3, "mod.lock", bundle.Yes),
			grantOpt(0, 2, 3, "mod.pin", bundle.No))
	})
	pool := dbtest.New(t)
	mustRun(t, pool, src, t.TempDir(), false)
	if got, want := optionRows(t, pool, "mod.pin"), []string{"user 2/3:1"}; !slices.Equal(got, want) {
		t.Errorf("mod.pin rows %v, want only the bundle's own %v", got, want)
	}
}
