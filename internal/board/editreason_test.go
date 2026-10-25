package board_test

import (
	"context"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// TestEditReasonBelongsToLastEditor: the reason on a post always belongs to the
// member who made the last edit. An edit that gives no reason clears the old one,
// whoever wrote it.
func TestEditReasonBelongsToLastEditor(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	a := viewerFor(t, pool, alice)
	m := moderator(t, pool, carol)

	// Post 1 is alice's. The import gave it the edit reason "typo". Her own edit
	// is a new edit, so the old reason must not show under her name.
	if got := readPost(t, pool, 1); got.EditReason != "typo" {
		t.Fatalf("fixture: edit_reason = %q, want the imported reason", got.EditReason)
	}
	if err := board.EditPost(ctx, pool, a, 1, board.Draft{Title: "t", Source: "own edit"}, "", t0); err != nil {
		t.Fatal(err)
	}
	if got := readPost(t, pool, 1); got.EditedBy != alice || got.EditReason != "" {
		t.Errorf("after the author's edit of an imported post: edited_by %d, edit_reason %q; want %d and none", got.EditedBy, got.EditReason, alice)
	}

	// A moderator edits with a reason: it is stored. Then the author edits with
	// none: the reason goes.
	if err := board.EditPost(ctx, pool, m, 1, board.Draft{Title: "t", Source: "cleaned"}, "removed a slur", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := readPost(t, pool, 1); got.EditedBy != carol || got.EditReason != "removed a slur" {
		t.Fatalf("after the moderator's edit: edited_by %d, edit_reason %q", got.EditedBy, got.EditReason)
	}
	if err := board.EditPost(ctx, pool, a, 1, board.Draft{Title: "t", Source: "alice again"}, "", t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := readPost(t, pool, 1); got.EditedBy != alice || got.EditReason != "" || got.EditCount != 4 {
		t.Errorf("after the author's second edit: edited_by %d, edit_reason %q, edit_count %d; want %d, none, 4", got.EditedBy, got.EditReason, got.EditCount, alice)
	}

	// The next moderator edit stores its own reason.
	if err := board.EditPost(ctx, pool, m, 1, board.Draft{Title: "t", Source: "cleaned 2"}, "off topic", t0.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := readPost(t, pool, 1); got.EditedBy != carol || got.EditReason != "off topic" {
		t.Errorf("after the second moderator edit: edited_by %d, edit_reason %q", got.EditedBy, got.EditReason)
	}
}
