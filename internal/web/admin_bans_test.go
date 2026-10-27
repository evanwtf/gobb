package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/bundletest"
)

// The ban rows of the small board come from the import and have the IDs 1 to 3.
// The tests count the bans that a test adds above them.
const importedBans = 3

// adminWithPass signs alice in, confirms her password, and returns her cookies.
func adminWithPass(t *testing.T, h http.Handler) []*http.Cookie {
	t.Helper()
	alice := signIn(t, h, "alice", testPassword)
	if rec, _ := postAs(t, h, "/admin/confirm", alice, url.Values{"password": {testPassword}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("confirm = %d", rec.Code)
	}
	return alice
}

func banCount(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), `select count(*) from bans`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// logRows reads the mod_log rows of the action as "actor target_id details".
func logRows(t *testing.T, pool *pgxpool.Pool, action string) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(), `select coalesce(actor_id, 0)::text || ' ' || target_type || ' ' || target_id || ' ' || details::text
  from mod_log where action = $1 order by id`, action)
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
	return out
}

func TestAdminBansPage(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	exec(t, pool, `insert into bans (kind, user_id, reason, reason_shown, created_at, created_by) values ('user', 3, '<b>staff</b>', 'Shown & told', now(), 1)`)
	alice := signIn(t, h, "alice", testPassword)

	rec := getAs(t, h, "/admin/bans", alice)
	body := bodyOf(t, rec)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/bans = %d", rec.Code)
	}
	for _, want := range []string{
		"<h1>Bans</h1>",
		`<a href="/u/3"><bdi>carol</bdi></a>`, // the member, linked
		"spammer@example.com", "10.99.0.0/16", // the imported email and range bans
		"&lt;b&gt;staff&lt;/b&gt;", "Shown &amp; told", // escaped
		`<a href="/u/1"><bdi>alice</bdi></a>`, // who added it
		"Command line or import",
		`action="/admin/bans/4/lift"`, `name="back" value="/admin/bans"`,
		`name="kind"`, `name="target"`, `name="reason"`, `name="reason_shown"`, `name="ends"`, `name="confirm_wide"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	// The ended ban of the import (user 4) is not in force.
	if strings.Contains(body, `href="/u/4"`) {
		t.Error("the page lists a ban that has ended")
	}
	if strings.Contains(body, "<b>staff</b>") {
		t.Error("the staff reason is not escaped")
	}
	// The link of the members page prefills the form.
	body = bodyOf(t, getAs(t, h, "/admin/bans?kind=user&target=bob%22%3E", alice))
	if !strings.Contains(body, `value="bob&#34;&gt;"`) || !strings.Contains(body, `<option value="user" selected>`) ||
		!strings.Contains(body, `name="back" value="/admin/bans?kind=user&amp;target=bob%22%3E"`) {
		t.Errorf("the form is not prefilled:\n%s", body)
	}
	body = bodyOf(t, getAs(t, h, "/admin/bans?kind=ip&target=192.0.2.0%2F24", alice))
	if !strings.Contains(body, `<option value="ip" selected>`) || !strings.Contains(body, `value="192.0.2.0/24"`) {
		t.Error("the ip form is not prefilled")
	}
	// A kind that is not one of the three falls back to the member.
	if body := bodyOf(t, getAs(t, h, "/admin/bans?kind=x", alice)); !strings.Contains(body, `<option value="user" selected>`) {
		t.Error("a bad kind is not ignored")
	}
	// The notices are fixed texts.
	for done, want := range map[string]string{"added": "The ban is added.", "lifted": "The ban is lifted.", "gone": "That ban is already gone."} {
		if b := bodyOf(t, getAs(t, h, "/admin/bans?done="+done, alice)); !strings.Contains(b, want) {
			t.Errorf("done=%s lacks %q", done, want)
		}
	}
	if b := bodyOf(t, getAs(t, h, "/admin/bans?done=%3Cb%3E", alice)); strings.Contains(b, "notice-inline") {
		t.Error("an unknown done value shows a notice")
	}
	if rec := getAs(t, h, "/admin/bans?page=9", alice); rec.Code != http.StatusNotFound {
		t.Errorf("page 9 = %d, want 404", rec.Code)
	}
	if rec := getAs(t, h, "/admin/bans?page=x", alice); rec.Code != http.StatusBadRequest {
		t.Errorf("page x = %d, want 400", rec.Code)
	}
	if !strings.Contains(bodyOf(t, getAs(t, h, "/admin/", alice)), `href="/admin/bans"`) {
		t.Error("the admin index does not link to the Bans page")
	}
}

// TestAdminBanAddNeedsPass: an add without a confirmed password goes to the
// confirm page, which returns to the page of the form, and changes nothing.
func TestAdminBanAddNeedsPass(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	alice := signIn(t, h, "alice", testPassword)
	before := dumpData(t, pool)
	rec, _ := postAs(t, h, "/admin/bans", alice, url.Values{"kind": {"user"}, "target": {"bob"}, "back": {"/admin/bans?kind=user&target=bob"}})
	want := "/admin/confirm?next=%2Fadmin%2Fbans%3Fkind%3Duser%26target%3Dbob"
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
		t.Errorf("without a pass = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	rec, _ = postAs(t, h, "/admin/bans/1/lift", alice, url.Values{"back": {"/admin/bans"}})
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/admin/confirm") {
		t.Errorf("lift without a pass = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	if diff := diffTables(before, dumpData(t, pool)); len(diff) != 0 {
		t.Errorf("a write without a pass changed %v", diff)
	}
}

func TestAdminBanAddEachKind(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	alice := adminWithPass(t, h)
	ny, _ := time.LoadLocation("America/New_York")
	end := time.Now().In(ny).Add(72 * time.Hour).Truncate(time.Minute)
	for i, c := range []struct {
		form    url.Values
		kind    string
		details string
	}{
		{url.Values{"kind": {"user"}, "target": {"Bob"}, "reason": {"spam"}, "reason_shown": {"Spam."}, "ends": {end.Format("2006-01-02T15:04")}}, "user", `"target": "bob"`},
		{url.Values{"kind": {"email"}, "target": {" Junk@Example.NET "}, "reason": {"bot"}}, "email", `"target": "junk@example.net"`},
		{url.Values{"kind": {"ip"}, "target": {"198.51.100.77/24"}, "reason": {"proxy"}}, "ip", `"target": "198.51.100.0/24"`},
		{url.Values{"kind": {"ip"}, "target": {"2001:db8::9"}}, "ip", `"target": "2001:db8::9"`},
	} {
		rec, _ := postAs(t, h, "/admin/bans", alice, c.form)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/bans?done=added" {
			t.Fatalf("add %d = %d to %q\n%s", i, rec.Code, rec.Header().Get("Location"), bodyOf(t, rec))
		}
		if n := banCount(t, pool); n != importedBans+i+1 {
			t.Errorf("add %d: %d bans", i, n)
		}
		rows := logRows(t, pool, "ban.add")
		last := rows[len(rows)-1]
		if len(rows) != i+1 || !strings.HasPrefix(last, "1 ban ") || !strings.Contains(last, `"via": "web"`) ||
			!strings.Contains(last, `"kind": "`+c.kind+`"`) || !strings.Contains(last, c.details) {
			t.Errorf("add %d: mod_log %q", i, rows)
		}
	}
	// The end is read in the zone of the admin.
	var got time.Time
	if err := pool.QueryRow(t.Context(), `select ends_at from bans where kind = 'user' and user_id = 2`).Scan(&got); err != nil || !got.Equal(end) {
		t.Errorf("ends_at = %v, %v; want %v", got, err, end)
	}
	body := bodyOf(t, getAs(t, h, "/admin/bans?done=added", alice))
	for _, want := range []string{"junk@example.net", "198.51.100.0/24", "2001:db8::9", `<a href="/u/2"><bdi>bob</bdi></a>`} {
		if !strings.Contains(body, want) {
			t.Errorf("the list lacks %q", want)
		}
	}
}

func TestAdminBanAddRefusals(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	alice := adminWithPass(t, h)
	// bob is a second admin who is not an owner.
	exec(t, pool, `insert into acl_user_grants (user_id, forum_id, option, setting) values (2, 0, 'admin', 3)`)
	exec(t, pool, `insert into bans (kind, user_id, created_at) values ('user', 3, now())`)
	before := dumpData(t, pool)
	tooLong := strings.Repeat("x", 256)
	for name, c := range map[string]struct {
		form    url.Values
		field   string // the id of the field that carries the message
		invalid string
	}{
		"no kind":         {url.Values{"target": {"bob"}}, "ban-kind", ""},
		"yourself":        {url.Values{"kind": {"user"}, "target": {"ALICE"}}, "ban-target", "You cannot ban yourself."},
		"an unknown name": {url.Values{"kind": {"user"}, "target": {"nobody"}}, "ban-target", "No member has that name."},
		"a banned member": {url.Values{"kind": {"user"}, "target": {"carol"}}, "ban-target", "That member is already banned."},
		"a bad address":   {url.Values{"kind": {"email"}, "target": {"not an address"}}, "ban-target", ""},
		"your address":    {url.Values{"kind": {"email"}, "target": {"Alice@example.com"}}, "ban-target", ""},
		"a bad range":     {url.Values{"kind": {"ip"}, "target": {"300.1.1.1"}}, "ban-target", ""},
		"your own range":  {url.Values{"kind": {"ip"}, "target": {"192.0.2.0/24"}, "confirm_wide": {"1"}}, "ban-target", "lock yourself out"},
		"a held range":    {url.Values{"kind": {"ip"}, "target": {"10.99.0.0/16"}}, "ban-target", "already"},
		"a wide v4 range": {url.Values{"kind": {"ip"}, "target": {"10.0.0.0/8"}}, "ban-wide", "Confirm the wide range"},
		"a wide v6 range": {url.Values{"kind": {"ip"}, "target": {"2001:db8::/32"}}, "ban-wide", "Confirm the wide range"},
		"a long reason":   {url.Values{"kind": {"user"}, "target": {"dave"}, "reason": {tooLong}}, "ban-reason", "too long"},
		"a long shown":    {url.Values{"kind": {"user"}, "target": {"dave"}, "reason_shown": {tooLong}}, "ban-shown", "too long"},
		"a bad end":       {url.Values{"kind": {"user"}, "target": {"dave"}, "ends": {"next week"}}, "ban-ends", "2030-01-31 18:00"},
		"an end in past":  {url.Values{"kind": {"user"}, "target": {"dave"}, "ends": {"2001-01-01T00:00"}}, "ban-ends", "future"},
	} {
		rec, _ := postAs(t, h, "/admin/bans", alice, c.form)
		body := bodyOf(t, rec)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, rec.Code)
			continue
		}
		errID := c.field + "-error"
		if !strings.Contains(body, `class="field-error" id="`+errID+`"`) || !strings.Contains(body, `aria-invalid="true"`) {
			t.Errorf("%s: no message beside %s", name, c.field)
		}
		if c.invalid != "" && !strings.Contains(body, c.invalid) {
			t.Errorf("%s: the page lacks %q", name, c.invalid)
		}
		// The form shows the values that the admin sent.
		if v := c.form.Get("target"); v != "" && !strings.Contains(body, `value="`+v+`"`) {
			t.Errorf("%s: the target is not kept", name)
		}
	}
	if diff := diffTables(before, dumpData(t, pool)); len(diff) != 0 {
		t.Errorf("a refused add changed %v", diff)
	}

	// An owner is refused for another admin; alice is the owner.
	bob := signIn(t, h, "bob", testPassword)
	if rec, _ := postAs(t, h, "/admin/confirm", bob, url.Values{"password": {testPassword}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("bob's confirm = %d", rec.Code)
	}
	before = dumpData(t, pool)
	for _, form := range []url.Values{
		{"kind": {"user"}, "target": {"alice"}},
		{"kind": {"email"}, "target": {"alice@example.com"}},
	} {
		rec, _ := postAs(t, h, "/admin/bans", bob, form)
		if body := bodyOf(t, rec); rec.Code != http.StatusBadRequest || !strings.Contains(body, "owner") {
			t.Errorf("a ban of the owner by %v = %d", form, rec.Code)
		}
	}
	if diff := diffTables(before, dumpData(t, pool)); len(diff) != 0 {
		t.Errorf("a refused add of the owner changed %v", diff)
	}
}

func TestAdminBanLiftEachKind(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	alice := adminWithPass(t, h)
	exec(t, pool, `insert into bans (kind, user_id, created_at) values ('user', 3, now())`) // id 4
	exec(t, pool, `delete from bans where id = 1`)
	for i, c := range []struct {
		id      string
		kind    string
		details string
	}{
		{"4", "user", `"target": "carol"`},
		{"2", "email", `"target": "spammer@example.com"`},
		{"3", "ip", `"target": "10.99.0.0/16"`},
	} {
		rec, _ := postAs(t, h, "/admin/bans/"+c.id+"/lift", alice, url.Values{"back": {"/admin/bans"}})
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/bans?done=lifted" {
			t.Fatalf("lift %s = %d to %q", c.id, rec.Code, rec.Header().Get("Location"))
		}
		var n int
		if err := pool.QueryRow(t.Context(), `select count(*) from bans where id = `+c.id).Scan(&n); err != nil || n != 0 {
			t.Errorf("ban %s: %d rows, %v", c.id, n, err)
		}
		rows := logRows(t, pool, "ban.lift")
		last := rows[len(rows)-1]
		if len(rows) != i+1 || !strings.HasPrefix(last, "1 ban "+c.id+" ") || !strings.Contains(last, `"via": "web"`) ||
			!strings.Contains(last, `"kind": "`+c.kind+`"`) || !strings.Contains(last, c.details) {
			t.Errorf("lift %s: mod_log %q", c.id, rows)
		}
	}
	// A ban that is gone, an ended ban and a bad ID.
	before := dumpData(t, pool)
	rec, _ := postAs(t, h, "/admin/bans/4/lift", alice, url.Values{})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/bans?done=gone" {
		t.Errorf("second lift = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	for _, id := range []string{"0", "-1", "x", "99999999999999999999"} {
		if rec, _ := postAs(t, h, "/admin/bans/"+id+"/lift", alice, url.Values{}); rec.Code != http.StatusNotFound && rec.Header().Get("Location") != "/admin/bans?done=gone" {
			t.Errorf("lift of %s = %d", id, rec.Code)
		}
	}
	if diff := diffTables(before, dumpData(t, pool)); len(diff) != 0 {
		t.Errorf("a refused lift changed %v", diff)
	}
}

// TestAdminBanEffects: a user ban ends the sessions of the member; a range ban
// refuses a sign-in from inside the range and lets one from outside through; an
// email ban refuses the member with that address. Lifting each lets them in.
func TestAdminBanEffects(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), proxyOptions())
	alice := adminWithPass(t, h)
	bobCookies := signIn(t, h, "bob", testPassword)

	// signInFrom tries to sign name in from addr and returns the status.
	signInFrom := func(name, addr string) (int, string) {
		t.Helper()
		cookies, token := csrfFor(t, h)
		rec := proxied(t, h, "/login", addr, cookies, url.Values{"name": {name}, "password": {testPassword}, "csrf": {token}})
		return rec.Code, bodyOf(t, rec)
	}
	add := func(form url.Values) {
		t.Helper()
		if rec, _ := postAs(t, h, "/admin/bans", alice, form); rec.Code != http.StatusSeeOther {
			t.Fatalf("add %v = %d\n%s", form, rec.Code, bodyOf(t, rec))
		}
	}
	lift := func(kind string) {
		t.Helper()
		var id string
		if err := pool.QueryRow(t.Context(), `select max(id)::text from bans where kind = $1`, kind).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if rec, _ := postAs(t, h, "/admin/bans/"+id+"/lift", alice, url.Values{}); rec.Code != http.StatusSeeOther {
			t.Fatalf("lift %s = %d", id, rec.Code)
		}
	}

	// A range ban.
	add(url.Values{"kind": {"ip"}, "target": {"198.51.100.0/24"}, "reason_shown": {"Proxy range."}})
	if code, body := signInFrom("carol", "198.51.100.200"); code != http.StatusForbidden || !strings.Contains(body, "Reason: Proxy range.") {
		t.Errorf("sign-in from inside the range = %d", code)
	}
	if code, _ := signInFrom("carol", "198.51.101.1"); code != http.StatusSeeOther {
		t.Errorf("sign-in from outside the range = %d, want 303", code)
	}
	if code, _ := signInFrom("carol", "198.51.99.255"); code != http.StatusSeeOther {
		t.Errorf("sign-in from below the range = %d, want 303", code)
	}
	lift("ip")
	if code, _ := signInFrom("carol", "198.51.100.200"); code != http.StatusSeeOther {
		t.Errorf("sign-in after the lift = %d, want 303", code)
	}
	// One address.
	add(url.Values{"kind": {"ip"}, "target": {"2001:db8::5"}})
	if code, _ := signInFrom("carol", "2001:db8::5"); code != http.StatusForbidden {
		t.Errorf("sign-in from the banned address = %d", code)
	}
	if code, _ := signInFrom("carol", "2001:db8::6"); code != http.StatusSeeOther {
		t.Errorf("sign-in from the next address = %d, want 303", code)
	}
	lift("ip")

	// An email ban, typed with capitals and spaces, holds the address of carol.
	add(url.Values{"kind": {"email"}, "target": {" CAROL@Example.com "}, "reason_shown": {"Address banned."}})
	if code, body := signInFrom("carol", "203.0.113.4"); code != http.StatusForbidden || !strings.Contains(body, "Reason: Address banned.") {
		t.Errorf("sign-in with a banned address = %d", code)
	}
	lift("email")
	if code, _ := signInFrom("carol", "203.0.113.4"); code != http.StatusSeeOther {
		t.Errorf("sign-in after the lift = %d, want 303", code)
	}

	// A user ban ends the sessions and refuses the sign-in.
	if rec := getAs(t, h, "/account", bobCookies); rec.Code != http.StatusOK {
		t.Fatalf("bob's account = %d", rec.Code)
	}
	add(url.Values{"kind": {"user"}, "target": {"bob"}, "reason_shown": {"Member banned."}})
	if rec := getAs(t, h, "/account", bobCookies); rec.Code == http.StatusOK {
		t.Error("bob's session survived the ban")
	}
	if code, body := signInFrom("bob", "203.0.113.4"); code != http.StatusForbidden || !strings.Contains(body, "Reason: Member banned.") {
		t.Errorf("sign-in of the banned member = %d", code)
	}
	lift("user")
	if code, _ := signInFrom("bob", "203.0.113.4"); code != http.StatusSeeOther {
		t.Errorf("sign-in after the lift = %d, want 303", code)
	}
}
