package mail_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/dbtest"
	"github.com/evanwtf/gobb/internal/mail"
	"github.com/evanwtf/gobb/internal/mail/mailtest"
)

// queueDB returns a migrated database with one member, and that member's ID.
func queueDB(t *testing.T) (*pgxpool.Pool, int64) {
	t.Helper()
	pool := dbtest.New(t)
	var id int64
	err := pool.QueryRow(t.Context(),
		`insert into users (username, username_clean, email, email_clean, password_hash,
		                    password_scheme, state, registered_at)
		 values ('alice', 'alice', $1, $1, 'x', 'gobb', 'active', $2) returning id`,
		recipient, now).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return pool, id
}

// clock is a time that a test moves.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// fakeSender calls fn for each message and counts the calls.
type fakeSender struct {
	mu    sync.Mutex
	calls int
	fn    func(ctx context.Context, m mail.Message) error
}

func (f *fakeSender) Send(ctx context.Context, m mail.Message) error {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.fn == nil {
		return nil
	}
	return f.fn(ctx, m)
}

func (f *fakeSender) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

var timeoutErr = context.DeadlineExceeded

var msg = mail.Message{To: recipient, Subject: "Reset your password on Secret Board", Body: "BODYLINE-7731\r\n"}

type rowState struct {
	attempts   int
	nextAt     time.Time
	sentAt     *time.Time
	droppedAt  *time.Time
	lastError  string
	body       string
	sendBy     *time.Time
	userID     *int64
	createdAt  time.Time
	subject    string
	recipients string
}

func readRow(t *testing.T, pool *pgxpool.Pool, id int64) rowState {
	t.Helper()
	var r rowState
	err := pool.QueryRow(t.Context(),
		`select attempts, next_attempt_at, sent_at, dropped_at, last_error, body, send_by,
		        user_id, created_at, subject, to_addr
		 from email_queue where id = $1`, id).
		Scan(&r.attempts, &r.nextAt, &r.sentAt, &r.droppedAt, &r.lastError, &r.body, &r.sendBy,
			&r.userID, &r.createdAt, &r.subject, &r.recipients)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func enqueue(t *testing.T, pool *pgxpool.Pool, uid int64, sendBy time.Time) int64 {
	t.Helper()
	id, err := mail.Enqueue(t.Context(), pool, msg, uid, sendBy, now)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestMigration0014(t *testing.T) {
	pool, uid := queueDB(t)
	ctx := t.Context()
	cols := map[string]string{}
	rows, err := pool.Query(ctx,
		`select table_name || '.' || column_name, is_nullable from information_schema.columns
		 where table_schema = current_schema()
		   and ((table_name = 'email_queue' and column_name in ('user_id', 'send_by', 'dropped_at'))
		     or table_name = 'password_resets')`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var c, null string
		if err := rows.Scan(&c, &null); err != nil {
			t.Fatal(err)
		}
		cols[c] = null
	}
	rows.Close()
	want := map[string]string{
		"email_queue.user_id": "YES", "email_queue.send_by": "YES", "email_queue.dropped_at": "YES",
		"password_resets.id": "NO", "password_resets.user_id": "NO", "password_resets.epoch": "NO",
		"password_resets.created_at": "NO", "password_resets.expires_at": "NO",
	}
	for c, null := range want {
		if cols[c] != null {
			t.Errorf("column %s: nullable = %q, want %q", c, cols[c], null)
		}
	}
	if len(cols) != len(want) {
		t.Errorf("columns = %v, want %v", cols, want)
	}

	enqueue(t, pool, uid, time.Time{})
	if _, err := pool.Exec(ctx,
		`insert into password_resets (id, user_id, epoch, created_at, expires_at)
		 values ('abc', $1, 0, $2, $2)`, uid, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `delete from users where id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	var q, r int
	if err := pool.QueryRow(ctx,
		`select (select count(*) from email_queue), (select count(*) from password_resets)`).
		Scan(&q, &r); err != nil {
		t.Fatal(err)
	}
	if q != 0 || r != 0 {
		t.Errorf("after the delete of the user: %d queue rows and %d reset rows, want 0 and 0", q, r)
	}
}

func TestEnqueueRefusesBadAddress(t *testing.T) {
	pool, uid := queueDB(t)
	_, err := mail.Enqueue(t.Context(), pool,
		mail.Message{To: "a@example.org\r\nBcc: b@example.org", Subject: "s", Body: "b"}, uid, time.Time{}, now)
	if err == nil {
		t.Fatal("Enqueue accepted a recipient with a line break")
	}
	var n int
	if err := pool.QueryRow(t.Context(), `select count(*) from email_queue`).Scan(&n); err != nil || n != 0 {
		t.Errorf("rows = %d (%v), want 0", n, err)
	}
}

func TestEnqueueStoresTheRow(t *testing.T) {
	pool, uid := queueDB(t)
	sendBy := now.Add(time.Hour)
	r := readRow(t, pool, enqueue(t, pool, uid, sendBy))
	if r.recipients != recipient || r.subject != msg.Subject || r.body != msg.Body {
		t.Errorf("row = %+v", r)
	}
	if r.userID == nil || *r.userID != uid || r.sendBy == nil || !r.sendBy.Equal(sendBy) {
		t.Errorf("user %v, send_by %v, want %d and %v", r.userID, r.sendBy, uid, sendBy)
	}
	if !r.nextAt.Equal(now) || !r.createdAt.Equal(now) || r.attempts != 0 {
		t.Errorf("next %v, created %v, attempts %d", r.nextAt, r.createdAt, r.attempts)
	}
}

func TestProcessSends(t *testing.T) {
	pool, uid := queueDB(t)
	srv, s := sender(t, mailtest.Options{}, "", "")
	id := enqueue(t, pool, uid, time.Time{})
	q := &mail.Queue{Pool: pool, Sender: s, Now: func() time.Time { return now }}

	sent, failed, dropped, err := q.Process(t.Context())
	if err != nil || sent != 1 || failed != 0 || dropped != 0 {
		t.Fatalf("Process = %d, %d, %d, %v; want 1, 0, 0, nil", sent, failed, dropped, err)
	}
	r := readRow(t, pool, id)
	if r.sentAt == nil || !r.sentAt.Equal(now) || r.body != "" || r.attempts != 1 || r.droppedAt != nil {
		t.Errorf("row = %+v", r)
	}
	if got := srv.Messages(); len(got) != 1 {
		t.Errorf("the server holds %d messages, want 1", len(got))
	}
	// A second pass finds nothing.
	if sent, _, _, _ := q.Process(t.Context()); sent != 0 {
		t.Errorf("a second pass sent %d", sent)
	}
}

func TestOutageRetriesThenDrops(t *testing.T) {
	pool, uid := queueDB(t)
	fs := &fakeSender{fn: func(context.Context, mail.Message) error { return timeoutErr }}
	clk := &clock{t: now}
	q := &mail.Queue{Pool: pool, Sender: fs, Now: clk.Now}
	id := enqueue(t, pool, uid, now.Add(time.Hour))

	steps := []struct{ at, next time.Duration }{
		{0, time.Minute}, {time.Minute, 5 * time.Minute}, {6 * time.Minute, 15 * time.Minute},
		{21 * time.Minute, 30 * time.Minute},
	}
	for i, s := range steps {
		clk.Set(now.Add(s.at))
		sent, failed, dropped, err := q.Process(t.Context())
		if err != nil || sent != 0 || failed != 1 || dropped != 0 {
			t.Fatalf("pass %d = %d, %d, %d, %v; want 0, 1, 0, nil", i+1, sent, failed, dropped, err)
		}
		r := readRow(t, pool, id)
		if want := now.Add(s.at + s.next); !r.nextAt.Equal(want) {
			t.Errorf("pass %d: next_attempt_at = %v, want %v", i+1, r.nextAt, want)
		}
		if r.attempts != i+1 || r.lastError != "timeout" || r.droppedAt != nil || r.body == "" {
			t.Errorf("pass %d: row = %+v", i+1, r)
		}
	}
	calls := fs.Calls()
	clk.Set(now.Add(61 * time.Minute)) // past send_by, and the row is due
	sent, failed, dropped, err := q.Process(t.Context())
	if err != nil || sent != 0 || failed != 0 || dropped != 1 {
		t.Fatalf("last pass = %d, %d, %d, %v; want 0, 0, 1, nil", sent, failed, dropped, err)
	}
	if fs.Calls() != calls {
		t.Error("the sender ran on the pass that dropped the row")
	}
	r := readRow(t, pool, id)
	if r.droppedAt == nil || r.body != "" || r.sentAt != nil {
		t.Errorf("row = %+v", r)
	}
}

func TestProcessDropsPermanent(t *testing.T) {
	pool, uid := queueDB(t)
	fs := &fakeSender{fn: func(context.Context, mail.Message) error {
		return &textproto.Error{Code: 550, Msg: "5.1.1 no such user"}
	}}
	q := &mail.Queue{Pool: pool, Sender: fs, Now: func() time.Time { return now }}
	id := enqueue(t, pool, uid, now.Add(time.Hour))
	sent, failed, dropped, err := q.Process(t.Context())
	if err != nil || sent != 0 || failed != 0 || dropped != 1 {
		t.Fatalf("Process = %d, %d, %d, %v; want 0, 0, 1, nil", sent, failed, dropped, err)
	}
	r := readRow(t, pool, id)
	if r.droppedAt == nil || r.body != "" || r.attempts != 1 || !strings.HasPrefix(r.lastError, "smtp 550") {
		t.Errorf("row = %+v", r)
	}
}

func TestProcessDropsAfterEight(t *testing.T) {
	pool, uid := queueDB(t)
	fs := &fakeSender{fn: func(context.Context, mail.Message) error { return timeoutErr }}
	clk := &clock{t: now}
	q := &mail.Queue{Pool: pool, Sender: fs, Now: clk.Now}
	id := enqueue(t, pool, uid, time.Time{})
	for i := 1; i <= 8; i++ {
		clk.Set(now.Add(time.Duration(i) * time.Hour))
		sent, failed, dropped, err := q.Process(t.Context())
		wantFailed, wantDropped := 1, 0
		if i == 8 {
			wantFailed, wantDropped = 0, 1
		}
		if err != nil || sent != 0 || failed != wantFailed || dropped != wantDropped {
			t.Fatalf("pass %d = %d, %d, %d, %v; want 0, %d, %d, nil", i, sent, failed, dropped, err, wantFailed, wantDropped)
		}
	}
	r := readRow(t, pool, id)
	if r.droppedAt == nil || r.attempts != 8 || r.body != "" {
		t.Errorf("row = %+v", r)
	}
	clk.Set(now.Add(100 * time.Hour))
	if _, f, d, _ := q.Process(t.Context()); f+d != 0 || fs.Calls() != 8 {
		t.Errorf("a dropped row ran again: %d calls", fs.Calls())
	}
}

// blocker is a Sender that waits for release.
type blocker struct {
	entered chan struct{}
	release chan struct{}
	fakeSender
}

func newBlocker() *blocker {
	b := &blocker{entered: make(chan struct{}, 10), release: make(chan struct{})}
	b.fn = func(ctx context.Context, _ mail.Message) error {
		b.entered <- struct{}{}
		select {
		case <-b.release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return b
}

func (b *blocker) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-b.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the sender did not run")
	}
}

type passResult struct {
	sent, failed, dropped int
	err                   error
}

func processAsync(ctx context.Context, q *mail.Queue) <-chan passResult {
	ch := make(chan passResult, 1)
	go func() {
		var r passResult
		r.sent, r.failed, r.dropped, r.err = q.Process(ctx)
		ch <- r
	}()
	return ch
}

func TestProcessLease(t *testing.T) {
	pool, uid := queueDB(t)
	b := newBlocker()
	clk := &clock{t: now}
	q := &mail.Queue{Pool: pool, Sender: b, Now: clk.Now}
	id := enqueue(t, pool, uid, time.Time{})

	first := processAsync(t.Context(), q)
	b.waitEntered(t)
	if r := readRow(t, pool, id); !r.nextAt.Equal(now.Add(5*time.Minute)) || r.attempts != 1 {
		t.Errorf("claimed row = %+v, want a lease to +5 min and 1 attempt", r)
	}
	for _, at := range []time.Duration{0, 4 * time.Minute} {
		clk.Set(now.Add(at))
		if sent, failed, dropped, err := q.Process(t.Context()); err != nil || sent+failed+dropped != 0 {
			t.Errorf("a pass at +%v = %d, %d, %d, %v; want nothing", at, sent, failed, dropped, err)
		}
	}
	close(b.release)
	select {
	case r := <-first:
		if r.err != nil || r.sent != 1 {
			t.Errorf("first pass = %+v, want 1 sent", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the first pass did not end")
	}
	if b.Calls() != 1 {
		t.Errorf("sender ran %d times, want 1", b.Calls())
	}
	if r := readRow(t, pool, id); r.sentAt == nil {
		t.Error("the row was not sent")
	}
}

// A stop of the server in the middle of a send is not a failure of the message.
func TestProcessShutdownLeavesTheRowClaimed(t *testing.T) {
	pool, uid := queueDB(t)
	b := newBlocker()
	clk := &clock{t: now}
	q := &mail.Queue{Pool: pool, Sender: b, Now: clk.Now}
	id := enqueue(t, pool, uid, now.Add(time.Hour))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	res := processAsync(ctx, q)
	b.waitEntered(t)
	cancel()
	var r passResult
	select {
	case r = <-res:
	case <-time.After(5 * time.Second):
		t.Fatal("Process ignored its context")
	}
	if r.failed != 0 || r.dropped != 0 || r.sent != 0 {
		t.Errorf("Process = %+v, want no outcome", r)
	}
	row := readRow(t, pool, id)
	if row.lastError != "" || row.droppedAt != nil || row.sentAt != nil || row.body == "" ||
		row.attempts != 1 || !row.nextAt.Equal(now.Add(5*time.Minute)) {
		t.Errorf("row = %+v, want it left as claimed", row)
	}

	// The lease runs out and a later pass sends the row.
	close(b.release)
	clk.Set(now.Add(5 * time.Minute))
	if sent, _, _, err := q.Process(t.Context()); err != nil || sent != 1 {
		t.Errorf("later pass sent %d (%v), want 1", sent, err)
	}
}

func TestProcessHoldsNoConnection(t *testing.T) {
	pool, uid := queueDB(t)
	cfg := pool.Config().Copy()
	cfg.MaxConns = 1
	one, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	b := newBlocker()
	q := &mail.Queue{Pool: one, Sender: b, Now: func() time.Time { return now }}
	enqueue(t, pool, uid, time.Time{})

	res := processAsync(t.Context(), q)
	b.waitEntered(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var n int
	if err := one.QueryRow(ctx, `select 1`).Scan(&n); err != nil {
		t.Errorf("select 1 while the sender blocks: %v", err)
	}
	close(b.release)
	if r := <-res; r.err != nil || r.sent != 1 {
		t.Errorf("Process = %+v", r)
	}
}

func TestQueueLogsNoSecret(t *testing.T) {
	pool, uid := queueDB(t)
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })

	results := []error{
		&textproto.Error{Code: 451, Msg: "4.2.0 <" + recipient + "> try later"},
		&textproto.Error{Code: 550, Msg: "5.1.1 <" + recipient + "> no such user"},
		nil,
	}
	i := 0
	fs := &fakeSender{fn: func(context.Context, mail.Message) error {
		defer func() { i++ }()
		return results[i]
	}}
	clk := &clock{t: now}
	q := &mail.Queue{Pool: pool, Sender: fs, Now: clk.Now}
	for range results {
		enqueue(t, pool, uid, time.Time{})
	}
	// The first row fails and waits; the second drops; the third sends. Rows are
	// claimed in order of ID, in one pass.
	if sent, failed, dropped, err := q.Process(t.Context()); err != nil || sent != 1 || failed != 1 || dropped != 1 {
		t.Fatalf("Process = %d, %d, %d, %v", sent, failed, dropped, err)
	}
	out := buf.String()
	for _, bad := range []string{"@", msg.Subject, "BODYLINE", "example.org"} {
		if strings.Contains(out, bad) {
			t.Errorf("log holds %q:\n%s", bad, out)
		}
	}
	for _, want := range []string{"email sent", "level=WARN", "smtp 451", "smtp 550", "user_id=" + strconv.FormatInt(uid, 10)} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
}

func TestRunWakes(t *testing.T) {
	pool, uid := queueDB(t)
	srv, s := sender(t, mailtest.Options{}, "", "")
	wake := make(chan struct{}, 1)
	q := &mail.Queue{Pool: pool, Sender: s, Wake: wake}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { q.Run(ctx); close(done) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run ignored its context")
		}
	}()

	time.Sleep(200 * time.Millisecond) // the first pass finds nothing
	id, err := mail.Enqueue(t.Context(), pool, msg, uid, time.Time{}, time.Now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	wake <- struct{}{}
	deadline := time.Now().Add(2 * time.Second)
	for readRow(t, pool, id).sentAt == nil {
		if time.Now().After(deadline) {
			t.Fatal("the row was not sent within 2 s of the wake")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := srv.Messages(); len(got) != 1 {
		t.Errorf("the server holds %d messages, want 1", len(got))
	}
}

func TestMailCleanup(t *testing.T) {
	pool, uid := queueDB(t)
	ctx := t.Context()
	day := 24 * time.Hour
	add := func(sentAt, droppedAt *time.Time) {
		t.Helper()
		if _, err := pool.Exec(ctx,
			`insert into email_queue (to_addr, created_at, next_attempt_at, sent_at, dropped_at, user_id)
			 values ($1, $2, $2, $3, $4, $5)`, recipient, now.Add(-60*day), sentAt, droppedAt, uid); err != nil {
			t.Fatal(err)
		}
	}
	at := func(d time.Duration) *time.Time { v := now.Add(-d); return &v }
	add(at(31*day), nil)
	add(nil, at(31*day))
	add(at(29*day), nil)
	add(nil, at(29*day))
	add(nil, nil)
	for _, exp := range []time.Time{now.Add(-time.Minute), now.Add(time.Minute)} {
		if _, err := pool.Exec(ctx,
			`insert into password_resets (id, user_id, epoch, created_at, expires_at)
			 values ($1, $2, 0, $3, $4)`, exp.Format(time.RFC3339), uid, now.Add(-time.Hour), exp); err != nil {
			t.Fatal(err)
		}
	}
	n, err := mail.Cleanup(ctx, pool, now)
	if err != nil || n != 3 {
		t.Fatalf("Cleanup = %d, %v; want 3 (two queue rows, one reset)", n, err)
	}
	var q, r int
	if err := pool.QueryRow(ctx,
		`select (select count(*) from email_queue), (select count(*) from password_resets)`).
		Scan(&q, &r); err != nil {
		t.Fatal(err)
	}
	if q != 3 || r != 1 {
		t.Errorf("left %d queue rows and %d resets, want 3 and 1", q, r)
	}
}
