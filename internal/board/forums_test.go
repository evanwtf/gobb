package board_test

import (
	"context"
	"testing"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/acl"
	"github.com/evanwtf/gobb/internal/acldb"
	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

func TestIndexGuest(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	s, err := board.LoadSettings(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	v, err := board.Guest(ctx, pool, s)
	if err != nil {
		t.Fatal(err)
	}
	forums, err := board.Index(ctx, pool, v)
	if err != nil {
		t.Fatal(err)
	}
	if len(forums) != 1 || forums[0].ID != 1 || forums[0].Name != "General" {
		t.Fatalf("top level = %+v, want only General (1)", forums)
	}
	cat := forums[0]
	if len(cat.Children) != 1 || cat.Children[0].ID != 2 {
		t.Fatalf("children of General = %+v, want only forum 2", cat.Children)
	}
	f := cat.Children[0]
	if len(f.Children) != 0 {
		t.Errorf("forum 2 has %d children for the guest, want 0", len(f.Children))
	}
	if f.Topics != 1 || f.Posts != 3 {
		t.Errorf("forum 2 counts = %d topics, %d posts, want 1 and 3", f.Topics, f.Posts)
	}
	if f.Description != "The main forum" || f.Kind != "forum" {
		t.Errorf("forum 2 = %+v", f)
	}
	if f.Last == nil || f.Last.ID != 4 || f.Last.Author.Name != "Visitor" || f.Last.Author.ID != 0 {
		t.Fatalf("forum 2 last = %+v, want post 4 by Visitor with no ID", f.Last)
	}
	if f.Last.TopicID != 1 || f.Last.TopicTitle != "Welcome to the example board" {
		t.Errorf("forum 2 last topic = %d %q", f.Last.TopicID, f.Last.TopicTitle)
	}
	// The category adds up the forums under it.
	if cat.Topics != 1 || cat.Posts != 3 {
		t.Errorf("General counts = %d and %d, want 1 and 3", cat.Topics, cat.Posts)
	}
}

func TestIndexRollsUpReadableOnly(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	s, err := board.LoadSettings(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	subject, err := acldb.Load(ctx, pool, 1)
	if err != nil {
		t.Fatal(err)
	}
	v := board.Viewer{UserID: 1, Name: "alice", Perms: acl.Resolve(subject), Scheme: s.DefaultScheme}
	forums, err := board.Index(ctx, pool, v)
	if err != nil {
		t.Fatal(err)
	}
	if len(forums) != 1 || len(forums[0].Children) != 1 {
		t.Fatalf("tree = %+v", forums)
	}
	f := forums[0].Children[0]
	if len(f.Children) != 1 || f.Children[0].ID != 3 {
		t.Fatalf("forum 2 children = %+v, want forum 3", f.Children)
	}
	if f.Topics != 2 || f.Posts != 5 {
		t.Errorf("forum 2 counts = %d topics, %d posts, want 2 and 5", f.Topics, f.Posts)
	}
	if f.Last == nil || f.Last.ID != 4 {
		t.Errorf("forum 2 last = %+v, want post 4, the newest", f.Last)
	}
	sub := f.Children[0]
	if sub.Topics != 1 || sub.Posts != 2 || sub.Last == nil || sub.Last.ID != 6 {
		t.Errorf("forum 3 = %d topics, %d posts, last %+v; want 1, 2, post 6", sub.Topics, sub.Posts, sub.Last)
	}
	if sub.Last != nil && (sub.Last.Author.ID != 1 || sub.Last.Author.Name != "alice") {
		t.Errorf("forum 3 last author = %+v", sub.Last.Author)
	}
}

// A hidden subforum adds nothing to its parent: not a count, not a last post.
func TestIndexHiddenAddsNothing(t *testing.T) {
	ctx := context.Background()
	b := bundletest.Small()
	// Make the hidden forum's post the newest on the board.
	for i := range b.Posts {
		if b.Posts[i].ID == 6 {
			b.Posts[i].CreatedAt = b.Posts[i].CreatedAt.AddDate(10, 0, 0)
		}
	}
	pool, _ := boardtest.Load(t, b)
	s, err := board.LoadSettings(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	v, err := board.Guest(ctx, pool, s)
	if err != nil {
		t.Fatal(err)
	}
	forums, err := board.Index(ctx, pool, v)
	if err != nil {
		t.Fatal(err)
	}
	f := forums[0].Children[0]
	if f.Topics != 1 || f.Posts != 3 || f.Last == nil || f.Last.ID != 4 {
		t.Errorf("forum 2 = %d, %d, %+v; the hidden forum leaked", f.Topics, f.Posts, f.Last)
	}
}

func TestIndexOrderAndLinks(t *testing.T) {
	ctx := context.Background()
	b := bundletest.Small()
	// A second top-level forum with no category, and a link forum after it.
	b.Forums = append(b.Forums,
		bundleForum(4, 0, "forum", "Loose", 2),
		bundleForum(5, 0, "link", "Elsewhere", 3),
		bundleForum(6, 1, "forum", "Early", 0),
	)
	b.Forums[4].LinkURL = ptr("https://example.com/")
	for _, id := range []int64{4, 5, 6} {
		b.Grants = append(b.Grants, listGrant(id), readGrant(id))
	}
	pool, _ := boardtest.Load(t, b)
	s, err := board.LoadSettings(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	v, err := board.Guest(ctx, pool, s)
	if err != nil {
		t.Fatal(err)
	}
	forums, err := board.Index(ctx, pool, v)
	if err != nil {
		t.Fatal(err)
	}
	var top []int64
	for _, f := range forums {
		top = append(top, f.ID)
	}
	if len(top) != 3 || top[0] != 1 || top[1] != 4 || top[2] != 5 {
		t.Fatalf("top level order = %v, want [1 4 5]", top)
	}
	if forums[2].Kind != "link" || forums[2].LinkURL != "https://example.com/" {
		t.Errorf("link forum = %+v", forums[2])
	}
	// Position 0 sorts before position 1 inside the category.
	kids := forums[0].Children
	if len(kids) != 2 || kids[0].ID != 6 || kids[1].ID != 2 {
		t.Errorf("category children = %v", kids)
	}
}

func ptr[T any](v T) *T { return &v }

func bundleForum(id, parent int64, kind, name string, pos int) bundle.Forum {
	f := bundle.Forum{ID: id, Kind: kind, Name: name, Position: pos}
	if parent != 0 {
		f.ParentID = &parent
	}
	return f
}

func optGrant(forum int64, option string) bundle.Grant {
	group := int64(1) // the guests group
	s := bundle.Yes
	return bundle.Grant{GroupID: &group, ForumID: forum, Option: &option, Setting: &s}
}

func listGrant(forum int64) bundle.Grant { return optGrant(forum, "forum.list") }
func readGrant(forum int64) bundle.Grant { return optGrant(forum, "forum.read") }
