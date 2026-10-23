package acl

import "testing"

func subj(grants ...Grant) Subject { return Subject{Grants: grants} }

func TestNeverBeatsYes(t *testing.T) {
	s := Resolve(subj(
		Grant{2, "forum.read", Yes},
		Grant{2, "forum.read", Never},
	))
	if s.Has("forum.read", 2) {
		t.Fatal("Never must beat Yes")
	}
}

func TestYesWithoutNever(t *testing.T) {
	s := Resolve(subj(Grant{2, "forum.read", Yes}, Grant{2, "forum.read", No}))
	if !s.Has("forum.read", 2) {
		t.Fatal("Yes without Never must give true")
	}
}

func TestUnsetAndNoAreFalse(t *testing.T) {
	s := Resolve(subj(
		Grant{2, "forum.read", No},
		Grant{2, "forum.list", Unset},
	))
	if s.Has("forum.read", 2) || s.Has("forum.list", 2) || s.Has("forum.search", 2) {
		t.Fatal("No, Unset and absent must give false")
	}
}

func TestForumScopeIgnoresOtherForum(t *testing.T) {
	s := Resolve(subj(Grant{2, "forum.read", Yes}))
	if !s.Has("forum.read", 2) {
		t.Fatal("forum 2 must have forum.read")
	}
	if s.Has("forum.read", 3) {
		t.Fatal("forum 3 must not have forum.read")
	}
}

func TestModeratorScopeBoardOrForum(t *testing.T) {
	board := Resolve(subj(Grant{0, "mod.delete", Yes}))
	if !board.Has("mod.delete", 7) {
		t.Fatal("a board-wide grant must reach forum 7")
	}
	forum := Resolve(subj(Grant{7, "mod.delete", Yes}))
	if !forum.Has("mod.delete", 7) {
		t.Fatal("a forum grant must reach its own forum")
	}
	if forum.Has("mod.delete", 8) {
		t.Fatal("a forum grant must not reach forum 8")
	}
}

func TestBoardScopeIgnoresForumGrant(t *testing.T) {
	s := Resolve(subj(Grant{5, "pm.send", Yes}))
	if s.Has("pm.send", 0) {
		t.Fatal("a forum grant must not give a board option")
	}
	if s.Has("pm.send", 5) {
		t.Fatal("a forum grant must not give a board option in the forum")
	}
}

func TestForumOptionIgnoresBoardWideGrant(t *testing.T) {
	s := Resolve(subj(Grant{0, "forum.read", Yes}))
	if s.Has("forum.read", 5) {
		t.Fatal("a board-wide grant of a forum option must not apply in forum 5")
	}
}

func TestOwnerIsAdminOverNever(t *testing.T) {
	s := Resolve(Subject{Owner: true, Grants: []Grant{{0, "admin", Never}}})
	if !s.IsAdmin() {
		t.Fatal("an owner must be admin even over Never")
	}
}

func TestOwnerForumRightsComeFromGrants(t *testing.T) {
	s := Resolve(Subject{Owner: true})
	if s.Has("forum.read", 2) {
		t.Fatal("an owner gets no forum right without a grant")
	}
}

func TestNonOwnerAdmin(t *testing.T) {
	s := Resolve(subj(Grant{0, "admin", Yes}))
	if !s.IsAdmin() {
		t.Fatal("admin Yes must give admin")
	}
	s = Resolve(subj(Grant{0, "admin", Yes}, Grant{0, "admin", Never}))
	if s.IsAdmin() {
		t.Fatal("admin Never must beat Yes for a non-owner")
	}
	if Resolve(Subject{}).IsAdmin() {
		t.Fatal("an empty subject must not be admin")
	}
}

func TestUnknownGrantOptionIsIgnored(t *testing.T) {
	s := Resolve(subj(Grant{0, "dropped.option", Yes}, Grant{2, "forum.read", Yes}))
	if !s.Has("forum.read", 2) {
		t.Fatal("known grants must still resolve")
	}
}

func TestHasUnknownOptionPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Has with an unknown option must panic")
		}
	}()
	Resolve(Subject{}).Has("no.such.option", 1)
}

func TestSettingValues(t *testing.T) {
	if Unset != 0 || No != 1 || Never != 2 || Yes != 3 {
		t.Fatal("Setting values are stored in the database; they must not change")
	}
}

func TestRegistry(t *testing.T) {
	if len(Options) != 48 {
		t.Fatalf("got %d options, want 48", len(Options))
	}
	count := map[Scope]int{}
	seen := map[string]bool{}
	for _, o := range Options {
		if seen[o.Name] {
			t.Errorf("duplicate option %q", o.Name)
		}
		seen[o.Name] = true
		switch o.Scope {
		case Forum, Board, Moderator:
			count[o.Scope]++
		default:
			t.Errorf("option %q has invalid scope %d", o.Name, o.Scope)
		}
		got, ok := Lookup(o.Name)
		if !ok || got != o {
			t.Errorf("Lookup(%q) = %v, %v", o.Name, got, ok)
		}
	}
	if count[Forum] != 23 || count[Moderator] != 6 || count[Board] != 19 {
		t.Errorf("scope counts %v, want forum 23, moderator 6, board 19", count)
	}
	if o, ok := Lookup("admin"); !ok || o.Scope != Board {
		t.Errorf("admin must be a Board option, got %v, %v", o, ok)
	}
	if _, ok := Lookup("no.such.option"); ok {
		t.Error("Lookup must fail for an unknown name")
	}
}
