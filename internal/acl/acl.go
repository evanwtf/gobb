// Package acl resolves permissions. It holds the option registry and the rule
// that combines grants. It has no database code: callers build a Subject from
// stored data and ask a Set.
package acl

import "fmt"

// Setting is one grant value. The database stores No, Never and Yes as 1, 2
// and 3. The zero value is Unset, never Never.
type Setting uint8

const (
	Unset Setting = 0
	No    Setting = 1
	Never Setting = 2
	Yes   Setting = 3
)

// Grant gives one setting for one option. ForumID 0 means board-wide.
type Grant struct {
	ForumID int64
	Option  string
	Setting Setting
}

// Subject holds what Resolve needs: the user's own grants plus the grants of
// every non-pending group, with roles already expanded.
type Subject struct {
	Owner  bool
	Grants []Grant
}

// Set is the resolved result. Build it once with Resolve.
type Set struct {
	// allowed[forumID][option] is true when the option resolves to Yes.
	// Forum 0 holds board-wide results.
	allowed map[int64]map[string]bool
}

// Resolve combines the grants of s. For each (forum, option), any Never gives
// false; otherwise any Yes gives true; otherwise false.
//
// Resolve skips a grant whose option is not in the registry, because stored
// data may hold an option that a later version dropped. It also skips a grant
// whose scope does not fit its option: a Forum option needs a forum, and a
// Board option needs ForumID 0. Moderator options fit both.
func Resolve(s Subject) Set {
	type key struct {
		forum  int64
		option string
	}
	yes := map[key]bool{}
	never := map[key]bool{}
	for _, g := range s.Grants {
		o, ok := Lookup(g.Option)
		if !ok || !fits(o.Scope, g.ForumID) {
			continue
		}
		k := key{g.ForumID, g.Option}
		switch g.Setting {
		case Never:
			never[k] = true
		case Yes:
			yes[k] = true
		}
	}
	allowed := map[int64]map[string]bool{}
	for k := range yes {
		if never[k] {
			continue
		}
		if allowed[k.forum] == nil {
			allowed[k.forum] = map[string]bool{}
		}
		allowed[k.forum][k.option] = true
	}
	if s.Owner {
		// An owner always has admin, even over a Never.
		if allowed[0] == nil {
			allowed[0] = map[string]bool{}
		}
		allowed[0]["admin"] = true
	}
	return Set{allowed: allowed}
}

func fits(scope Scope, forumID int64) bool {
	switch scope {
	case Forum:
		return forumID != 0
	case Board:
		return forumID == 0
	default:
		return true
	}
}

// Has reports whether the subject has option in forumID. A Forum option reads
// the forum result. A Board option reads the board-wide result and ignores
// forumID. A Moderator option reads the board-wide result OR the forum result.
// Has panics when option is not in the registry: that is a programming error.
func (s Set) Has(option string, forumID int64) bool {
	o, ok := Lookup(option)
	if !ok {
		panic(fmt.Sprintf("acl: unknown option %q", option))
	}
	switch o.Scope {
	case Forum:
		return s.allowed[forumID][option]
	case Board:
		return s.allowed[0][option]
	default:
		return s.allowed[0][option] || s.allowed[forumID][option]
	}
}

// IsAdmin reports whether the subject has the admin right.
func (s Set) IsAdmin() bool {
	return s.Has("admin", 0)
}
