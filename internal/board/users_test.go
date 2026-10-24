package board_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/markup"
)

// treeText joins the text of every node of tree.
func treeText(n *markup.Node) string {
	if n == nil {
		return ""
	}
	s := n.Text
	for _, c := range n.Children {
		s += treeText(c)
	}
	return s
}

// names lists the names of the cards in order.
func names(cards []board.UserCard) []string {
	out := make([]string, len(cards))
	for i, c := range cards {
		out[i] = c.Name
	}
	return out
}

// hiddenGroupBoard is the small board with a hidden group, "Secret society", that
// alice and bob belong to. Its name must never reach a page.
func hiddenGroupBoard() *bundletest.Board {
	b := bundletest.Small()
	b.Groups = append(b.Groups, bundle.Group{ID: 6, Name: "Secret society", Kind: "hidden", Description: "Not for the list"})
	b.GroupMembers = append(b.GroupMembers,
		bundle.GroupMember{GroupID: 6, UserID: 1},
		bundle.GroupMember{GroupID: 6, UserID: 2, Leader: true},
	)
	return b
}

// manyMembers adds n active members named member001 to memberN. Each has
// post_count equal to the number in the name and registered one day apart.
func manyMembers(b *bundletest.Board, n int) {
	proto := b.Users[2] // carol
	for i := 1; i <= n; i++ {
		u := proto
		u.ID = int64(100 + i)
		u.Username = fmt.Sprintf("member%03d", i)
		u.Email = fmt.Sprintf("member%03d@example.com", i)
		u.RegisteredAt = time.Date(2021, 1, i%28+1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute)
		u.PostCount = i
		u.LastVisitAt = nil
		b.Users = append(b.Users, u)
		b.GroupMembers = append(b.GroupMembers, bundle.GroupMember{GroupID: 2, UserID: u.ID})
	}
}

func TestProfile(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	bob := memberOn(t, pool, 2, "bob")
	guest := guestOn(t, pool)

	p, err := board.Profile(ctx, pool, bob, 1)
	if err != nil {
		t.Fatal(err)
	}
	c := p.Card
	if c.ID != 1 || c.Name != "alice" || c.RankTitle != "Newcomer" || c.RankImage != "ranks/newcomer.png" ||
		c.PostCount != 120 || c.AvatarPath != "avatars/1.png" {
		t.Errorf("card = %+v", c)
	}
	if !c.Joined.Equal(time.Date(2019, 5, 1, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("Joined = %v", c.Joined)
	}
	if p.LastVisit == nil || !p.LastVisit.Equal(time.Date(2026, 10, 1, 8, 30, 0, 0, time.UTC)) {
		t.Errorf("LastVisit = %v", p.LastVisit)
	}
	if !slices.Equal(p.Groups, []string{"admins", "members"}) {
		t.Errorf("Groups = %v, want [admins members]", p.Groups)
	}
	if !strings.Contains(treeText(p.Signature), "Example signature") {
		t.Errorf("Signature = %q", treeText(p.Signature))
	}

	// Carol never visited, has no signature, and her request to join Beta testers
	// is pending.
	p, err = board.Profile(ctx, pool, bob, 3)
	if err != nil {
		t.Fatal(err)
	}
	if p.LastVisit != nil || p.Signature != nil || !slices.Equal(p.Groups, []string{"members"}) {
		t.Errorf("carol = last visit %v, signature %v, groups %v", p.LastVisit, p.Signature, p.Groups)
	}
	// A confirmed member of a group that members may join shows it.
	if p, err = board.Profile(ctx, pool, bob, 2); err != nil || !slices.Equal(p.Groups, []string{"Beta testers", "members", "moderators"}) {
		t.Errorf("bob = %v, %v", p.Groups, err)
	}

	// An inactive member cannot sign in, but the posts are on the board and so is the
	// profile. The member list still leaves the member out (see TestMembers).
	dave, err := board.Profile(ctx, pool, bob, 4)
	if err != nil {
		t.Fatalf("inactive user: %v", err)
	}
	if dave.Card.Name != "dave" || dave.Card.ID != 4 || !slices.Equal(dave.Groups, []string{"members"}) {
		t.Errorf("dave = %+v, groups %v", dave.Card, dave.Groups)
	}
	if _, err := board.Profile(ctx, pool, guest, 4); !errors.Is(err, board.ErrForbidden) {
		t.Errorf("guest, inactive user: err = %v, want ErrForbidden", err)
	}

	for _, c := range []struct {
		name string
		v    board.Viewer
		id   int64
		want error
	}{
		{"guest, a member", guest, 1, board.ErrForbidden},
		{"guest, an unknown ID", guest, 99, board.ErrForbidden},
		{"guest, a gone user", guest, 8, board.ErrGone},
		{"bob, a gone user", bob, 8, board.ErrGone},
		{"bob, an unknown ID", bob, 99, board.ErrNotFound},
	} {
		if _, err := board.Profile(ctx, pool, c.v, c.id); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
}

// A hidden group is not listed on a profile, whoever looks, and a pending
// membership is not listed either.
func TestProfileHidesHiddenGroups(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, hiddenGroupBoard())
	for _, viewer := range []board.Viewer{memberOn(t, pool, 1, "alice"), memberOn(t, pool, 2, "bob")} {
		for _, id := range []int64{1, 2} {
			p, err := board.Profile(ctx, pool, viewer, id)
			if err != nil {
				t.Fatal(err)
			}
			if slices.Contains(p.Groups, "Secret society") {
				t.Errorf("viewer %d sees the hidden group on profile %d: %v", viewer.UserID, id, p.Groups)
			}
		}
	}
}

// A profile costs three statements: the removed-user check, the member and the
// groups.
func TestProfileStatementCount(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	c := counting{pool: pool}
	if _, err := board.Profile(ctx, &c, memberOn(t, pool, 2, "bob"), 1); err != nil {
		t.Fatal(err)
	}
	if c.n != 3 {
		t.Errorf("Profile ran %d statements, want 3", c.n)
	}
}

func TestMembers(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	bob := memberOn(t, pool, 2, "bob")

	for _, c := range []struct {
		sort     string
		wantSort string
		want     []string
	}{
		{"", "name", []string{"alice", "bob", "carol"}},
		{"name", "name", []string{"alice", "bob", "carol"}},
		{"posts", "posts", []string{"alice", "bob", "carol"}},
		{"joined", "joined", []string{"alice", "bob", "carol"}},
		{"nonsense", "name", []string{"alice", "bob", "carol"}},
		{"posts; drop table users", "name", []string{"alice", "bob", "carol"}},
	} {
		mp, err := board.Members(ctx, pool, bob, c.sort, 1)
		if err != nil {
			t.Fatalf("sort %q: %v", c.sort, err)
		}
		if mp.Sort != c.wantSort || mp.Page != 1 || mp.Pages != 1 || !slices.Equal(names(mp.Members), c.want) {
			t.Errorf("sort %q = %q, page %d of %d, %v", c.sort, mp.Sort, mp.Page, mp.Pages, names(mp.Members))
		}
	}
	// dave is inactive and does not show.
	mp, err := board.Members(ctx, pool, bob, "name", 1)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(names(mp.Members), "dave") {
		t.Error("an inactive user is listed")
	}
	a := mp.Members[0]
	if a.ID != 1 || a.RankTitle != "Newcomer" || a.PostCount != 120 || a.AvatarPath != "avatars/1.png" {
		t.Errorf("alice's card = %+v", a)
	}
	// Carol has no rank row of her own; the rank for her post count shows.
	if mp.Members[2].RankTitle != "Newcomer" {
		t.Errorf("carol's rank = %q", mp.Members[2].RankTitle)
	}

	if _, err := board.Members(ctx, pool, guestOn(t, pool), "", 1); !errors.Is(err, board.ErrForbidden) {
		t.Errorf("guest err = %v, want ErrForbidden", err)
	}
	for _, page := range []int{0, -1, 2} {
		if _, err := board.Members(ctx, pool, bob, "", page); !errors.Is(err, board.ErrNotFound) {
			t.Errorf("page %d err = %v, want ErrNotFound", page, err)
		}
	}
}

// 133 active members make three pages of 50. Each sort orders the whole list, and
// the number of statements does not depend on the number of members.
func TestMembersPaging(t *testing.T) {
	ctx := context.Background()
	b := bundletest.Small()
	manyMembers(b, 130)
	pool, _ := boardtest.Load(t, b)
	bob := memberOn(t, pool, 2, "bob")

	p1, err := board.Members(ctx, pool, bob, "name", 1)
	if err != nil {
		t.Fatal(err)
	}
	p3, err := board.Members(ctx, pool, bob, "name", 3)
	if err != nil {
		t.Fatal(err)
	}
	if p1.Pages != 3 || len(p1.Members) != 50 || len(p3.Members) != 33 {
		t.Errorf("pages = %d, sizes %d and %d; want 3, 50 and 33", p1.Pages, len(p1.Members), len(p3.Members))
	}
	// alice, bob, carol, then member001 on.
	if got := names(p1.Members)[:4]; !slices.Equal(got, []string{"alice", "bob", "carol", "member001"}) {
		t.Errorf("first names = %v", got)
	}
	if last := p3.Members[len(p3.Members)-1].Name; last != "member130" {
		t.Errorf("last name = %q", last)
	}
	if _, err := board.Members(ctx, pool, bob, "name", 4); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("page 4 err = %v, want ErrNotFound", err)
	}

	posts, err := board.Members(ctx, pool, bob, "posts", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(posts.Members)[:4]; !slices.Equal(got, []string{"member130", "member129", "member128", "member127"}) {
		t.Errorf("by posts = %v", got)
	}
	for i := 1; i < len(posts.Members); i++ {
		if posts.Members[i].PostCount > posts.Members[i-1].PostCount {
			t.Fatalf("posts rise at %d: %d after %d", i, posts.Members[i].PostCount, posts.Members[i-1].PostCount)
		}
	}
	// Join dates: alice (2019) and bob (2019) come first, then carol (2020).
	joined, err := board.Members(ctx, pool, bob, "joined", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(joined.Members)[:3]; !slices.Equal(got, []string{"alice", "bob", "carol"}) {
		t.Errorf("by joined = %v", got)
	}
	for i := 1; i < len(joined.Members); i++ {
		if joined.Members[i].Joined.Before(joined.Members[i-1].Joined) {
			t.Fatalf("join dates fall at %d", i)
		}
	}

	small, _ := boardtest.Load(t, bundletest.Small())
	for _, sortBy := range []string{"name", "joined", "posts"} {
		many := counting{pool: pool}
		if _, err := board.Members(ctx, &many, bob, sortBy, 2); err != nil {
			t.Fatal(err)
		}
		few := counting{pool: small}
		if _, err := board.Members(ctx, &few, memberOn(t, small, 2, "bob"), sortBy, 1); err != nil {
			t.Fatal(err)
		}
		if many.n != 2 || few.n != 2 {
			t.Errorf("sort %s: %d statements for 133 members, %d for 3; want 2 for both", sortBy, many.n, few.n)
		}
	}
}

func TestTeam(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	groups, err := board.Team(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	// alice is an owner and an admin: she shows once, under Owners. The admins
	// group is then empty and does not show.
	if len(groups) != 2 || groups[0].Name != "Owners" || groups[1].Name != "moderators" {
		t.Fatalf("groups = %+v", groups)
	}
	if !slices.Equal(names(groups[0].Members), []string{"alice"}) || !slices.Equal(names(groups[1].Members), []string{"bob"}) {
		t.Errorf("members = %v and %v", names(groups[0].Members), names(groups[1].Members))
	}
	if a := groups[0].Members[0]; a.ID != 1 || a.RankTitle != "Newcomer" || a.AvatarPath != "avatars/1.png" {
		t.Errorf("alice's card = %+v", a)
	}
}

// Each person shows once, in the first group that holds them; a pending member,
// an inactive user and a member of a hidden group do not show for that reason.
func TestTeamDedupesAndSkips(t *testing.T) {
	ctx := context.Background()
	b := hiddenGroupBoard()
	b.GroupMembers = append(b.GroupMembers,
		bundle.GroupMember{GroupID: 4, UserID: 2},                // bob is an admin and a moderator
		bundle.GroupMember{GroupID: 4, UserID: 3},                // carol is an admin
		bundle.GroupMember{GroupID: 3, UserID: 3, Pending: true}, // pending moderators do not count
		bundle.GroupMember{GroupID: 3, UserID: 4},                // dave is inactive
	)
	pool, _ := boardtest.Load(t, b)
	groups, err := board.Team(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, g := range groups {
		got = append(got, g.Name+":"+strings.Join(names(g.Members), ","))
	}
	want := []string{"Owners:alice", "admins:bob,carol"}
	if !slices.Equal(got, want) {
		t.Errorf("team = %v, want %v", got, want)
	}
}

func TestTeamStatementCount(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	c := counting{pool: pool}
	if _, err := board.Team(context.Background(), &c); err != nil {
		t.Fatal(err)
	}
	if c.n != 1 {
		t.Errorf("Team ran %d statements, want 1", c.n)
	}
}

func TestPage(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	p, err := board.Page(ctx, pool, "about")
	if err != nil {
		t.Fatal(err)
	}
	if p.Slug != "about" || p.Title != "About" || !strings.Contains(treeText(p.Tree), "This is an example board.") ||
		!p.UpdatedAt.Equal(time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("page = %+v, text %q", p, treeText(p.Tree))
	}
	if _, err := board.Page(ctx, pool, "nope"); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestPageLinks(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	c := counting{pool: pool}
	links, err := board.PageLinks(ctx, &c)
	if err != nil {
		t.Fatal(err)
	}
	if c.n != 1 {
		t.Errorf("PageLinks ran %d statements, want 1", c.n)
	}
	if len(links) != 2 || links[0].Slug != "about" || links[0].Title != "About" || links[1].Slug != "rules" || links[1].Title != "Rules" {
		t.Errorf("links = %+v", links)
	}
	for _, l := range links {
		if l.Tree != nil {
			t.Errorf("link %s holds a tree", l.Slug)
		}
	}
}
