package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/evanwtf/gobb/internal/board"
)

// The groups pages of the admin area: the list of groups with a form to create
// one, and the page of one group with its details, its members and its
// requests. A change of the members changes what they may do, so it is a
// dangerous route: it needs a confirmed password, like a grant.

// groupDone are the notices that ?done=... can show on a groups page.
var groupDone = map[string]string{
	"created":  "The group is created.",
	"saved":    "The group is saved.",
	"deleted":  "The group is deleted.",
	"member":   "The members are saved.",
	"nochange": "Nothing changed: it was set that way already.",
}

// groupMemberActs are the changes that /admin/groups/{id}/members/{user}/{act}
// takes. An add goes through the form, by name.
var groupMemberActs = map[string]board.GroupMemberAction{
	"remove": board.GroupRemove, "lead": board.GroupLead, "unlead": board.GroupUnlead,
	"approve": board.GroupApprove, "refuse": board.GroupRefuse,
}

// adminGroupRoutes adds the routes of the groups pages.
func (s *site) adminGroupRoutes(a adminRoutes) {
	a.page("/groups", s.adminGroups)
	a.write("/groups", maxAccountBody, s.adminGroupCreate)
	a.page("/groups/{id}", s.adminGroup)
	a.write("/groups/{id}", maxAccountBody, s.adminGroupSave)
	a.write("/groups/{id}/delete", maxPostBody, s.adminGroupDelete)
	a.dangerous("/groups/{id}/members", maxPostBody, s.adminGroupAdd)
	a.dangerous("/groups/{id}/members/{user}/{act}", maxPostBody, s.adminGroupMember)
}

// groupForm holds the values of a group form as sent, and the messages beside
// its fields.
type groupForm struct {
	Name, Kind, Description          string
	NameErr, KindErr, DescriptionErr string
}

// adminGroupsData is the data of admin-groups.html.
type adminGroupsData struct {
	Crumbs []crumb
	Groups []board.AdminGroup
	Kinds  []string
	Form   groupForm
	// Done is the notice of a change that succeeded, Notice the message of a
	// failed write.
	Done, Notice string
}

// groupCrumbs returns the trail Index > Admin > Groups.
func (s *site) groupCrumbs() []crumb {
	return append(s.adminCrumbs(), crumb{Label: "Groups", Href: s.path("admin", "groups")})
}

// adminGroups serves GET /admin/groups.
func (s *site) adminGroups(w http.ResponseWriter, r *http.Request) {
	s.renderGroups(w, r, http.StatusOK, adminGroupsData{Done: groupDone[r.URL.Query().Get("done")], Form: groupForm{Kind: "open"}})
}

// renderGroups writes the list of groups with d.
func (s *site) renderGroups(w http.ResponseWriter, r *http.Request, status int, d adminGroupsData) {
	p, err := s.adminBegin(w, r, "Groups")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d.Groups, err = board.AdminGroups(r.Context(), s.pool)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d.Crumbs, d.Kinds = s.adminCrumbs(), board.GroupKinds
	p.Data = d
	s.render(w, status, "admin-groups.html", p)
}

// formOf reads the group form of r.
func formOf(r *http.Request) groupForm {
	f := r.PostForm
	return groupForm{Name: f.Get("name"), Kind: f.Get("kind"), Description: f.Get("description")}
}

// fieldError puts the message of in beside its field of f.
func (f *groupForm) fieldError(in *board.InputError) {
	switch in.Field {
	case "kind":
		f.KindErr = in.Message
	case "description":
		f.DescriptionErr = in.Message
	default:
		f.NameErr = in.Message
	}
}

// adminGroupCreate serves POST /admin/groups: 303 to the new group, or the list
// again with 400 and the message beside the field.
func (s *site) adminGroupCreate(w http.ResponseWriter, r *http.Request) {
	p, err := s.adminBegin(w, r, "Groups")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	f := formOf(r)
	id, err := board.CreateGroup(r.Context(), s.pool, p.Viewer, board.GroupInput{Name: f.Name, Kind: f.Kind, Description: f.Description}, s.opt.Now())
	var in *board.InputError
	switch {
	case errors.As(err, &in):
		f.fieldError(in)
		s.renderGroups(w, r, http.StatusBadRequest, adminGroupsData{Form: f})
	case errors.Is(err, board.ErrForbidden):
		s.notFound(w, r)
	case err != nil:
		slog.Error("admin group write failed", "path", r.URL.Path, "err", err)
		s.renderGroups(w, r, failStatus(r, err), adminGroupsData{Form: f, Notice: msgWriteFailed})
	default:
		http.Redirect(w, r, s.groupHref(id)+"?done=created", http.StatusSeeOther)
	}
}

// groupHref returns the site path of the page of group id.
func (s *site) groupHref(id int64) string {
	return s.path("admin", "groups", strconv.FormatInt(id, 10))
}

// adminGroupData is the data of admin-group.html.
type adminGroupData struct {
	Crumbs  []crumb
	Group   board.AdminGroup
	Members []groupRow
	Kinds   []string
	Form    groupForm
	// AddName is the name in the add form as sent, and AddErr its message.
	AddName, AddErr string
	// Done is the notice of a change that succeeded, Notice the message of a
	// refusal.
	Done, Notice string
	// NeedsPass is true when a change of the members needs a confirmed password
	// first. Href is the site path of this page.
	NeedsPass bool
	Href      string
}

// groupRow is one member of the group page, with the buttons for it.
type groupRow struct {
	board.GroupMember
	Acts []groupAct
}

// groupAct is one button of a member: the last part of its path, and its label.
type groupAct struct{ Act, Label string }

// groupActsFor returns the buttons of member m: approve and refuse for a
// request, and leader and remove for a member.
func groupActsFor(m board.GroupMember) []groupAct {
	switch {
	case m.Pending:
		return []groupAct{{"approve", "Approve"}, {"refuse", "Refuse"}}
	case m.Leader:
		return []groupAct{{"unlead", "Not a leader"}, {"remove", "Remove"}}
	}
	return []groupAct{{"lead", "Make a leader"}, {"remove", "Remove"}}
}

// adminGroup serves GET /admin/groups/{id}.
func (s *site) adminGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	s.renderGroup(w, r, id, http.StatusOK, adminGroupData{Done: groupDone[r.URL.Query().Get("done")]})
}

// renderGroup writes the page of group id with d. The form holds the stored
// values when d has none. A group that does not exist is the 404 page.
func (s *site) renderGroup(w http.ResponseWriter, r *http.Request, id int64, status int, d adminGroupData) {
	p, err := s.adminBegin(w, r, "Group")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	g, members, err := board.AdminGroupDetail(r.Context(), s.pool, id)
	switch {
	case errors.Is(err, board.ErrNotFound):
		s.notFound(w, r)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	p.Title = "Group: " + g.Name
	d.Crumbs, d.Group, d.Kinds = s.groupCrumbs(), g, board.GroupKinds
	for _, m := range members {
		d.Members = append(d.Members, groupRow{GroupMember: m, Acts: groupActsFor(m)})
	}
	d.Href, d.NeedsPass = s.groupHref(id), !s.passFresh(r)
	if d.Form == (groupForm{}) {
		d.Form = groupForm{Name: g.Name, Kind: g.Kind, Description: g.Description}
	}
	p.Data = d
	s.render(w, status, "admin-group.html", p)
}

// adminGroupSave serves POST /admin/groups/{id}: the name, the kind and the
// description.
func (s *site) adminGroupSave(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	p, err := s.adminBegin(w, r, "Group")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	f := formOf(r)
	err = board.UpdateGroup(r.Context(), s.pool, p.Viewer, id, board.GroupInput{Name: f.Name, Kind: f.Kind, Description: f.Description}, s.opt.Now())
	var in *board.InputError
	if errors.As(err, &in) {
		f.fieldError(in)
		s.renderGroup(w, r, id, http.StatusBadRequest, adminGroupData{Form: f})
		return
	}
	s.groupAnswer(w, r, id, err, "saved", adminGroupData{Form: f})
}

// adminGroupDelete serves POST /admin/groups/{id}/delete: 303 to the list.
func (s *site) adminGroupDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	p, err := s.adminBegin(w, r, "Group")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	err = board.DeleteGroup(r.Context(), s.pool, p.Viewer, id, s.opt.Now())
	if err == nil {
		http.Redirect(w, r, s.path("admin", "groups")+"?done=deleted", http.StatusSeeOther)
		return
	}
	s.groupAnswer(w, r, id, err, "", adminGroupData{})
}

// adminGroupAdd serves POST /admin/groups/{id}/members: add a member by name.
func (s *site) adminGroupAdd(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	p, err := s.adminBegin(w, r, "Group")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	name := strings.TrimSpace(r.PostForm.Get("name"))
	userID, _, err := board.UserByName(r.Context(), s.pool, name)
	switch {
	case errors.Is(err, board.ErrNotFound):
		s.renderGroup(w, r, id, http.StatusBadRequest, adminGroupData{AddName: name, AddErr: "No member has that name."})
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	err = board.ChangeGroupMember(r.Context(), s.pool, p.Viewer, id, userID, board.GroupAdd, s.opt.Now())
	s.groupAnswer(w, r, id, err, "member", adminGroupData{AddName: name})
}

// adminGroupMember serves POST /admin/groups/{id}/members/{user}/{act}.
func (s *site) adminGroupMember(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	userID, okUser := pathID(r, "user")
	act, okAct := groupMemberActs[r.PathValue("act")]
	if !ok || !okUser || !okAct {
		s.notFound(w, r)
		return
	}
	p, err := s.adminBegin(w, r, "Group")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	err = board.ChangeGroupMember(r.Context(), s.pool, p.Viewer, id, userID, act, s.opt.Now())
	s.groupAnswer(w, r, id, err, "member", adminGroupData{})
}

// groupAnswer answers a write to group id: 303 to its page with the notice done
// when err is nil, and the page again with a message for a refusal.
func (s *site) groupAnswer(w http.ResponseWriter, r *http.Request, id int64, err error, done string, d adminGroupData) {
	status := http.StatusConflict
	switch {
	case err == nil:
		http.Redirect(w, r, s.groupHref(id)+"?done="+done, http.StatusSeeOther)
		return
	case errors.Is(err, board.ErrNoChange):
		http.Redirect(w, r, s.groupHref(id)+"?done=nochange", http.StatusSeeOther)
		return
	case errors.Is(err, board.ErrForbidden):
		s.notFound(w, r)
		return
	case errors.Is(err, board.ErrNotFound):
		d.Notice, status = "That group or member does not exist.", http.StatusNotFound
	case errors.Is(err, board.ErrBuiltinGroup):
		d.Notice = "A built-in group keeps its name and its kind, and cannot be deleted. The guests group takes no members."
	case errors.Is(err, board.ErrGroupInUse):
		d.Notice = "Only a group with no members, no requests and no grants can be deleted."
	case errors.Is(err, board.ErrOwnerOnly):
		d.Notice, status = "Only an owner can change the groups of an owner.", http.StatusForbidden
	case errors.Is(err, board.ErrLastAdmin):
		d.Notice = "The board needs at least one active admin, so this change was not made."
	default:
		slog.Error("admin group write failed", "path", r.URL.Path, "err", err)
		d.Notice, status = msgWriteFailed, failStatus(r, err)
	}
	s.renderGroup(w, r, id, status, d)
}
