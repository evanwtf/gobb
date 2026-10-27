package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestConversationPage(t *testing.T) {
	h, pool := messageSite(t, Options{MessageSends: 1000, MemberPosts: 1000})
	alice := signIn(t, h, "alice", testPassword)
	bob := signIn(t, h, "bob", testPassword)

	// alice starts a talk; bob answers from the reply form on the page.
	first := sentID(t, sendMessage(t, h, alice, "bob", "Lunch?", "Noon at the usual place?"))
	page := asPageAs(t, h, "/messages/"+strconv.FormatInt(first, 10), bob)
	firstText := strconv.FormatInt(first, 10)
	for _, want := range []string{`id="m` + firstText + `"`, `name="reply_to" value="` + firstText + `"`,
		`name="to" value="alice"`, `name="subject" value="Re: Lunch?"`, "Noon at the usual place?"} {
		if !strings.Contains(page, want) {
			t.Errorf("bob's page lacks %q", want)
		}
	}
	rec, _ := postAs(t, h, "/messages/new", bob, url.Values{"reply_to": {firstText}, "to": {"alice"}, "subject": {"Re: Lunch?"}, "body": {"Noon is fine."}})
	reply := sentID(t, rec)
	replyText := strconv.FormatInt(reply, 10)
	if loc := rec.Header().Get("Location"); loc != "/messages/"+replyText+"#m"+replyText {
		t.Errorf("the send goes to %q", loc)
	}
	var cid int64
	if err := pool.QueryRow(t.Context(), `select conversation_id from pms where id = $1`, reply).Scan(&cid); err != nil || cid != first {
		t.Fatalf("the reply is in conversation %d, %v; want %d", cid, err, first)
	}

	// alice sees both sides on one page, oldest first, from either message.
	for _, id := range []string{firstText, replyText} {
		page = asPageAs(t, h, "/messages/"+id, alice)
		i, j := strings.Index(page, "Noon at the usual place?"), strings.Index(page, "Noon is fine.")
		if i < 0 || j < 0 || i > j {
			t.Errorf("alice's page from %s does not hold both messages in order", id)
		}
		// The reply form answers the last message.
		if !strings.Contains(page, `name="reply_to" value="`+replyText+`"`) {
			t.Errorf("alice's reply form from %s does not answer %s", id, replyText)
		}
	}
	// The list holds one conversation for the two messages.
	list := bodyOf(t, getAs(t, h, "/messages", alice))
	if n := strings.Count(list, "Lunch?</bdi>"); n != 1 || !strings.Contains(list, "2 messages") {
		t.Errorf("alice's list shows the talk %d times:\n%s", n, list)
	}
	// Opening the page marked alice's copy of the reply read.
	var unread int
	if err := pool.QueryRow(t.Context(), `select count(*) from pm_recipients where pm_id = $1 and not is_read`, reply).Scan(&unread); err != nil || unread != 0 {
		t.Errorf("the reply is unread after alice opened the page: %d, %v", unread, err)
	}
}

func TestConversationReplyToOthersMessage(t *testing.T) {
	h, pool := messageSite(t, Options{MessageSends: 1000, MemberPosts: 1000})
	bob := signIn(t, h, "bob", testPassword)
	// Message 2 went from bob to alice. carol is not a party; a reply_to of 2
	// from her starts its own conversation, and so does one that is not a number.
	carol := signIn(t, h, "carol", testPassword)
	for _, replyTo := range []string{"2", "x", "-1"} {
		rec, _ := postAs(t, h, "/messages/new", carol, url.Values{"reply_to": {replyTo}, "to": {"bob"}, "subject": {"Hi " + replyTo}, "body": {"Hello " + replyTo}})
		id := sentID(t, rec)
		var cid int64
		if err := pool.QueryRow(t.Context(), `select conversation_id from pms where id = $1`, id).Scan(&cid); err != nil || cid != id {
			t.Errorf("reply_to %q: conversation %d, %v; want its own", replyTo, cid, err)
		}
	}
	if rec := getAs(t, h, "/messages/1?page=x", bob); rec.Code != http.StatusBadRequest {
		t.Errorf("page x = %d, want 400", rec.Code)
	}
}

func TestConversationPagerReachesPageOne(t *testing.T) {
	h, pool := messageSite(t, Options{MessageSends: 1000, MemberPosts: 1000})
	alice := signIn(t, h, "alice", testPassword)
	first := sentID(t, sendMessage(t, h, alice, "bob", "Long talk", "Message 0"))
	exec(t, pool, fmt.Sprintf(`insert into pms (author_id, author_name, created_at, subject, body_tree, body_source, conversation_id)
		select 1, 'alice', now(), 'Re: Long talk', '{}'::jsonb, 'x', %d from generate_series(1, 26)`, first))
	var last int64
	if err := pool.QueryRow(t.Context(), `select max(id) from pms`).Scan(&last); err != nil {
		t.Fatal(err)
	}
	// From the last message the page is 2, and its link to page 1 opens the page
	// with the first message.
	page := bodyOf(t, getAs(t, h, fmt.Sprintf("/messages/%d", last), alice))
	firstHref := fmt.Sprintf(`href="/messages/%d"`, first)
	if !strings.Contains(page, firstHref) || strings.Contains(page, `id="m`+strconv.FormatInt(first, 10)+`"`) {
		t.Fatalf("page 2 lacks a link to page 1, or holds the first message")
	}
	page = bodyOf(t, getAs(t, h, fmt.Sprintf("/messages/%d", first), alice))
	if !strings.Contains(page, `id="m`+strconv.FormatInt(first, 10)+`"`) || !strings.Contains(page, fmt.Sprintf(`href="/messages/%d?page=2"`, first)) {
		t.Errorf("page 1 lacks the first message or the link to page 2")
	}
}
