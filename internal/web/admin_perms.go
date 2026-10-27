package web

import (
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/evanwtf/gobb/internal/acl"
	"github.com/evanwtf/gobb/internal/board"
)

// The permission pages of the admin area: for each forum and board-wide, the
// grants of the groups; the grants of single members; the role editor; and a
// preview of what one member may do. Every write is a dangerous route: it needs
// a confirmed password.

// permDone are the notices that ?done=... can show on a permission page.
var permDone = map[string]string{
	"saved":    "The change is saved.",
	"nochange": "Nothing changed: it was set that way already.",
}

// settingValues maps the values of a setting list to settings.
var settingValues = map[string]acl.Setting{"unset": acl.Unset, "yes": acl.Yes, "no": acl.No, "never": acl.Never}

// settingLabel names a setting on a page.
func settingLabel(s acl.Setting) string {
	switch s {
	case acl.Yes:
		return "Yes"
	case acl.No:
		return "No"
	case acl.Never:
		return "Never"
	}
	return "Not set"
}

// adminPermRoutes adds the routes of the permission pages.
func (s *site) adminPermRoutes(a adminRoutes) {
	a.page("/permissions", s.adminPerms)
	a.page("/permissions/members", s.adminUserGrants)
	a.page("/permissions/{forum}", s.adminGrid)
	a.page("/roles", s.adminRoles)
	a.page("/roles/{id}", s.adminRole)
	a.dangerous("/permissions/{forum}/role", maxPostBody, s.adminGridRole)
	a.dangerous("/permissions/{forum}/option", maxPostBody, s.adminGridOption)
	a.dangerous("/permissions/members/add", maxPostBody, s.adminUserGrantAdd)
	a.dangerous("/permissions/revoke", maxPostBody, s.adminRevoke)
	a.dangerous("/roles/{id}", maxAccountBody, s.adminRoleSave)
}

// permCrumbs returns the trail Index > Admin > Permissions.
func (s *site) permCrumbs() []crumb {
	return append(s.adminCrumbs(), crumb{Label: "Permissions", Href: s.path("admin", "permissions")})
}

// adminPermsData is the data of admin-perms.html.
type adminPermsData struct {
	Crumbs []crumb
	Forums []board.ForumName
	// The preview form: the name and the forum as sent, and the result.
	PreviewName  string
	PreviewWhere int64
	Previewed    bool
	Allowed      []string
	Notice       string
}

// adminPerms serves GET /admin/permissions: the list of forums, and the preview
// of what one member may do.
func (s *site) adminPerms(w http.ResponseWriter, r *http.Request) {
	p, err := s.adminBegin(w, r, "Permissions")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	ctx := r.Context()
	d := adminPermsData{Crumbs: s.adminCrumbs()}
	if d.Forums, err = board.ForumNames(ctx, s.pool); err != nil {
		s.serverError(w, r, err)
		return
	}
	q := r.URL.Query()
	status := http.StatusOK
	if d.PreviewName = strings.TrimSpace(q.Get("user")); d.PreviewName != "" {
		d.PreviewWhere, _ = strconv.ParseInt(q.Get("forum"), 10, 64)
		id, _, err := board.UserByName(ctx, s.pool, d.PreviewName)
		switch {
		case errors.Is(err, board.ErrNotFound):
			d.Notice, status = "No member has that name.", http.StatusBadRequest
		case err != nil:
			s.serverError(w, r, err)
			return
		default:
			d.Allowed, err = board.Preview(ctx, s.pool, id, d.PreviewWhere)
			if err != nil {
				s.serverError(w, r, err)
				return
			}
			d.Previewed = true
		}
	}
	p.Data = d
	s.render(w, status, "admin-perms.html", p)
}

// adminGridData is the data of admin-grid.html.
type adminGridData struct {
	Crumbs []crumb
	board.PermissionGrid
	// Href is the site path of this page, and the base of its forms.
	Href string
	// Options are the options that a grant here can hold.
	Options      []string
	Done, Notice string
	NeedsPass    bool
	SettingLabel func(acl.Setting) string
}

// forumParam reads the {forum} of the path: a forum ID, or 0 for board-wide.
func forumParam(r *http.Request) (int64, bool) {
	v := r.PathValue("forum")
	if v == "0" {
		return 0, true
	}
	return pathID(r, "forum")
}

// adminGrid serves GET /admin/permissions/{forum}.
func (s *site) adminGrid(w http.ResponseWriter, r *http.Request) {
	forumID, ok := forumParam(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	s.renderGrid(w, r, forumID, http.StatusOK, permDone[r.URL.Query().Get("done")], "")
}

// renderGrid writes the grid of forumID with a notice.
func (s *site) renderGrid(w http.ResponseWriter, r *http.Request, forumID int64, status int, done, notice string) {
	p, err := s.adminBegin(w, r, "Permissions")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	grid, err := board.Grid(r.Context(), s.pool, forumID)
	switch {
	case errors.Is(err, board.ErrNotFound):
		s.notFound(w, r)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	if forumID == 0 {
		p.Title = "Board-wide permissions"
	} else {
		p.Title = "Permissions: " + grid.ForumName
	}
	p.Data = adminGridData{
		Crumbs: s.permCrumbs(), PermissionGrid: grid, Href: s.path("admin", "permissions", strconv.FormatInt(forumID, 10)),
		Options: board.OptionsFor(forumID), Done: done, Notice: notice, NeedsPass: !s.passFresh(r), SettingLabel: settingLabel,
	}
	s.render(w, status, "admin-grid.html", p)
}

// formID reads a positive ID from the form field name.
func formID(r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(r.PostForm.Get(name)), 10, 64)
	return id, err == nil && id > 0
}

// adminGridRole serves POST /admin/permissions/{forum}/role: give a role to a
// group there (on=1), or take it away.
func (s *site) adminGridRole(w http.ResponseWriter, r *http.Request) {
	forumID, ok := forumParam(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	group, okG := formID(r, "group")
	role, okR := formID(r, "role")
	if !okG || !okR {
		s.renderGrid(w, r, forumID, http.StatusBadRequest, "", "Pick a group and a role.")
		return
	}
	p, err := s.adminBegin(w, r, "Permissions")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	err = board.SetRoleGrant(r.Context(), s.pool, p.Viewer, board.Holder{GroupID: group}, forumID, role, r.PostForm.Get("on") == "1", s.opt.Now())
	s.permAnswer(w, r, err, s.path("admin", "permissions", strconv.FormatInt(forumID, 10)), func(status int, msg string) {
		s.renderGrid(w, r, forumID, status, "", msg)
	})
}

// adminGridOption serves POST /admin/permissions/{forum}/option: set one option
// for a group there.
func (s *site) adminGridOption(w http.ResponseWriter, r *http.Request) {
	forumID, ok := forumParam(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	group, okG := formID(r, "group")
	setting, okS := settingValues[r.PostForm.Get("setting")]
	if !okG || !okS {
		s.renderGrid(w, r, forumID, http.StatusBadRequest, "", "Pick a group, an option and a setting.")
		return
	}
	p, err := s.adminBegin(w, r, "Permissions")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	err = board.SetOptionGrant(r.Context(), s.pool, p.Viewer, board.Holder{GroupID: group}, forumID, r.PostForm.Get("option"), setting, s.opt.Now())
	s.permAnswer(w, r, err, s.path("admin", "permissions", strconv.FormatInt(forumID, 10)), func(status int, msg string) {
		s.renderGrid(w, r, forumID, status, "", msg)
	})
}

// permAnswer answers the result of a permission write: 303 to back with a
// notice on success or no change, the page again with a message on a refusal.
func (s *site) permAnswer(w http.ResponseWriter, r *http.Request, err error, back string, again func(status int, msg string)) {
	switch {
	case err == nil:
		http.Redirect(w, r, back+"?done=saved", http.StatusSeeOther)
	case errors.Is(err, board.ErrNoChange):
		http.Redirect(w, r, back+"?done=nochange", http.StatusSeeOther)
	case errors.Is(err, board.ErrScope):
		again(http.StatusBadRequest, "That grant does not fit here: a forum option or a forum role needs a forum, and a board option or a board role is board-wide.")
	case errors.Is(err, board.ErrLastAdmin):
		again(http.StatusConflict, "The board needs at least one active admin, so this change was not made.")
	case errors.Is(err, board.ErrOwnerOnly):
		again(http.StatusForbidden, "Only an owner can change the grants of an owner.")
	case errors.Is(err, board.ErrNotFound):
		again(http.StatusBadRequest, "That group, member, forum, role or grant does not exist.")
	case errors.Is(err, board.ErrForbidden):
		s.notFound(w, r)
	default:
		slog.Error("admin permission write failed", "path", r.URL.Path, "err", err)
		again(failStatus(r, err), msgWriteFailed)
	}
}

// adminUserGrantsData is the data of admin-user-grants.html.
type adminUserGrantsData struct {
	Crumbs       []crumb
	Grants       []board.Grant
	Forums       []board.ForumName
	Roles        []board.Role
	Options      []string
	Done, Notice string
	NeedsPass    bool
	// Name, Forum, Role, Option and Setting are the values of the add form.
	Name, Option, Setting string
	Forum, Role           int64
	SettingLabel          func(acl.Setting) string
}

// adminUserGrants serves GET /admin/permissions/members.
func (s *site) adminUserGrants(w http.ResponseWriter, r *http.Request) {
	s.renderUserGrants(w, r, http.StatusOK, adminUserGrantsData{Done: permDone[r.URL.Query().Get("done")]})
}

// renderUserGrants writes the page of the member grants with d.
func (s *site) renderUserGrants(w http.ResponseWriter, r *http.Request, status int, d adminUserGrantsData) {
	p, err := s.adminBegin(w, r, "Member grants")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	ctx := r.Context()
	if d.Grants, err = board.UserGrants(ctx, s.pool); err == nil {
		if d.Forums, err = board.ForumNames(ctx, s.pool); err == nil {
			d.Roles, err = board.Roles(ctx, s.pool)
		}
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	for _, o := range acl.Options {
		d.Options = append(d.Options, o.Name)
	}
	d.Crumbs, d.NeedsPass, d.SettingLabel = s.permCrumbs(), !s.passFresh(r), settingLabel
	p.Data = d
	s.render(w, status, "admin-user-grants.html", p)
}

// adminUserGrantAdd serves POST /admin/permissions/members/add: a role, or one
// option with a setting, for one member on a forum or board-wide.
func (s *site) adminUserGrantAdd(w http.ResponseWriter, r *http.Request) {
	f := r.PostForm
	d := adminUserGrantsData{Name: strings.TrimSpace(f.Get("name")), Option: f.Get("option"), Setting: f.Get("setting")}
	d.Forum, _ = strconv.ParseInt(f.Get("forum"), 10, 64)
	d.Role, _ = strconv.ParseInt(f.Get("role"), 10, 64)
	again := func(status int, msg string) {
		d.Notice = msg
		s.renderUserGrants(w, r, status, d)
	}
	setting, okS := settingValues[d.Setting]
	if d.Forum < 0 || (d.Role == 0 && (d.Option == "" || !okS || setting == acl.Unset)) {
		again(http.StatusBadRequest, "Pick a role, or an option and a setting.")
		return
	}
	p, err := s.adminBegin(w, r, "Member grants")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	ctx := r.Context()
	id, _, err := board.UserByName(ctx, s.pool, d.Name)
	switch {
	case errors.Is(err, board.ErrNotFound):
		again(http.StatusBadRequest, "No member has that name.")
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	h := board.Holder{UserID: id}
	if d.Role != 0 {
		err = board.SetRoleGrant(ctx, s.pool, p.Viewer, h, d.Forum, d.Role, true, s.opt.Now())
	} else {
		err = board.SetOptionGrant(ctx, s.pool, p.Viewer, h, d.Forum, d.Option, setting, s.opt.Now())
	}
	s.permAnswer(w, r, err, s.path("admin", "permissions", "members"), again)
}

// adminRevoke serves POST /admin/permissions/revoke: remove one grant of a
// group (kind=group) or of a member (kind=user), then go back to the admin page
// in "back".
func (s *site) adminRevoke(w http.ResponseWriter, r *http.Request) {
	back := strings.SplitN(s.adminNext(r.PostForm.Get("back")), "?", 2)[0]
	grant, ok := formID(r, "grant")
	kind := r.PostForm.Get("kind")
	if !ok || (kind != "user" && kind != "group") {
		s.fail(w, r, http.StatusBadRequest, "Bad request", "The form names no grant.")
		return
	}
	p, err := s.adminBegin(w, r, "Permissions")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	err = board.RevokeGrant(r.Context(), s.pool, p.Viewer, kind == "user", grant, s.opt.Now())
	s.permAnswer(w, r, err, back, func(status int, msg string) {
		s.fail(w, r, status, "Not changed", msg)
	})
}

// adminRolesData is the data of admin-roles.html.
type adminRolesData struct {
	Crumbs []crumb
	Roles  []board.Role
}

// adminRoles serves GET /admin/roles.
func (s *site) adminRoles(w http.ResponseWriter, r *http.Request) {
	p, err := s.adminBegin(w, r, "Roles")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	roles, err := board.Roles(r.Context(), s.pool)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	p.Data = adminRolesData{Crumbs: s.permCrumbs(), Roles: roles}
	s.render(w, http.StatusOK, "admin-roles.html", p)
}

// adminRoleData is the data of admin-role.html.
type adminRoleData struct {
	Crumbs []crumb
	board.Role
	// Rows are the options that the role can hold, each with its setting.
	Rows []roleRow
	// Outside are the stored options that the kind of the role cannot hold
	// with an effect. A save removes them.
	Outside      []string
	Href         string
	Done, Notice string
	NeedsPass    bool
}

// roleRow is one option of the role editor.
type roleRow struct {
	Option  string
	Setting string
}

// adminRole serves GET /admin/roles/{id}.
func (s *site) adminRole(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	s.renderRole(w, r, id, http.StatusOK, permDone[r.URL.Query().Get("done")], "")
}

// renderRole writes the editor of role id with a notice.
func (s *site) renderRole(w http.ResponseWriter, r *http.Request, id int64, status int, done, notice string) {
	p, err := s.adminBegin(w, r, "Role")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	role, err := board.RoleByID(r.Context(), s.pool, id)
	switch {
	case errors.Is(err, board.ErrNotFound):
		s.notFound(w, r)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	p.Title = "Role: " + role.Name
	d := adminRoleData{Crumbs: append(s.permCrumbs(), crumb{Label: "Roles", Href: s.path("admin", "roles")}), Role: role,
		Href: s.path("admin", "roles", strconv.FormatInt(id, 10)), Done: done, Notice: notice, NeedsPass: !s.passFresh(r)}
	allowed := board.RoleOptions(role.Kind)
	for _, o := range allowed {
		d.Rows = append(d.Rows, roleRow{Option: o, Setting: strings.ToLower(settingLabel(role.Settings[o]))})
	}
	for o := range role.Settings {
		if !slices.Contains(allowed, o) {
			d.Outside = append(d.Outside, o)
		}
	}
	p.Data = d
	s.render(w, status, "admin-role.html", p)
}

// adminRoleSave serves POST /admin/roles/{id}: the setting of each option is the
// form field "o:<option>", one of unset, yes, no or never. A missing field
// means unset.
func (s *site) adminRoleSave(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	settings := map[string]acl.Setting{}
	for k, v := range r.PostForm {
		option, found := strings.CutPrefix(k, "o:")
		if !found {
			continue
		}
		setting, ok := settingValues[strings.Join(v, "")]
		if !ok || len(v) != 1 {
			s.renderRole(w, r, id, http.StatusBadRequest, "", "Each option must be Not set, Yes, No or Never.")
			return
		}
		settings[option] = setting
	}
	p, err := s.adminBegin(w, r, "Role")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	err = board.UpdateRole(r.Context(), s.pool, p.Viewer, id, settings, s.opt.Now())
	if errors.Is(err, board.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	s.permAnswer(w, r, err, s.path("admin", "roles", strconv.FormatInt(id, 10)), func(status int, msg string) {
		s.renderRole(w, r, id, status, "", msg)
	})
}
