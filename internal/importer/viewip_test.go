package importer

import (
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/dbtest"
)

// TestImportDropsViewIPFromRoles: a Yes for mod.view_ip in a role does not
// load, and the import warns once for each role that held it, by name. A No or
// a Never in a role stays, and so do the grants of mod.view_ip to a user or a
// group, and the admin right.
func TestImportDropsViewIPFromRoles(t *testing.T) {
	src := writeBoard(t, func(b *bundletest.Board) {
		b.Roles = append(b.Roles,
			bundle.Role{ID: 3, Name: "Full moderator", Kind: "mod", Position: 3,
				Settings: []bundle.RoleSetting{
					{Option: "mod.edit", Setting: bundle.Yes},
					{Option: "mod.view_ip", Setting: bundle.Yes},
				}},
			bundle.Role{ID: 4, Name: "Address only", Kind: "mod", Position: 4,
				Settings: []bundle.RoleSetting{{Option: "mod.view_ip", Setting: bundle.Yes}}},
			bundle.Role{ID: 5, Name: "No addresses", Kind: "mod", Position: 5,
				Settings: []bundle.RoleSetting{{Option: "mod.view_ip", Setting: bundle.Never}}},
		)
		role := int64(3)
		group := int64(3)
		b.Grants = append(b.Grants,
			bundle.Grant{GroupID: &group, ForumID: 2, RoleID: &role},
			grantOpt(0, 1, 0, "mod.view_ip", bundle.Yes),
			grantOpt(4, 0, 2, "mod.view_ip", bundle.Yes))
	})

	logs := &logRecords{}
	old := slog.Default()
	slog.SetDefault(slog.New(logs))
	t.Cleanup(func() { slog.SetDefault(old) })
	pool := dbtest.New(t)
	mustRun(t, pool, src, t.TempDir(), false)

	// Never is 2, Yes is 3.
	want := []string{"group 4/2:3", "role 5:2", "user 1/0:3"}
	if got := optionRows(t, pool, "mod.view_ip"); !slices.Equal(got, want) {
		t.Errorf("mod.view_ip rows = %v, want %v", got, want)
	}
	if got := optionRows(t, pool, "mod.edit"); !slices.Contains(got, "role 3:3") {
		t.Errorf("mod.edit rows = %v: the other settings of the role must stay", got)
	}
	if got := optionRows(t, pool, "admin"); !slices.Contains(got, "group 4/0:3") {
		t.Errorf("admin rows = %v: the admin right must stay", got)
	}

	var warned, kept []string
	for _, r := range logs.records {
		attrs := map[string]string{}
		r.Attrs(func(a slog.Attr) bool { attrs[a.Key] = a.Value.String(); return true })
		switch {
		case r.Level == slog.LevelWarn && r.Message == "import: removed mod.view_ip from role; members who hold this role no longer see post addresses":
			warned = append(warned, attrs["role"]+" grants="+attrs["grants"])
		case r.Level == slog.LevelInfo && r.Message == "import: kept a direct grant of mod.view_ip; this holder sees post addresses":
			kept = append(kept, attrs["holder"]+" "+attrs["id"]+" "+attrs["name"]+" forum="+attrs["forum_id"])
		}
	}
	slices.Sort(warned)
	if want := []string{"Address only grants=0", "Full moderator grants=1"}; !slices.Equal(warned, want) {
		t.Errorf("warnings name %v, want %v", warned, want)
	}
	if want := []string{"group 4 admins forum=2", "user 1 alice forum=0"}; !slices.Equal(kept, want) {
		t.Errorf("kept holders logged %v, want %v", kept, want)
	}
}

// TestImportViewIPReportWaitsForCommit: an import that fails after the roles
// load rolls back, and logs no removed role.
func TestImportViewIPReportWaitsForCommit(t *testing.T) {
	src := writeBoard(t, func(b *bundletest.Board) {
		b.Roles = append(b.Roles, bundle.Role{ID: 3, Name: "Full moderator", Kind: "mod", Position: 3,
			Settings: []bundle.RoleSetting{{Option: "mod.view_ip", Setting: bundle.Yes}}})
	})
	// A post loads after the roles; its tree fails, so the transaction rolls back.
	defer breakOn("Hello from a guest.")()
	logs := &logRecords{}
	old := slog.Default()
	slog.SetDefault(slog.New(logs))
	t.Cleanup(func() { slog.SetDefault(old) })
	pool := dbtest.New(t)
	if _, err := Run(t.Context(), pool, src, Options{DataDir: t.TempDir()}); err == nil {
		t.Fatal("the import with a broken parser did not fail")
	}
	for _, r := range logs.records {
		if strings.Contains(r.Message, "mod.view_ip") {
			t.Errorf("a failed import logged %q", r.Message)
		}
	}
}
