package web

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/bundletest"
)

// A client that half-closes its connection ends the request context of the
// server, and can still read the answer. A failed sign-in must still take the
// floor, or the client could time the answer to the hash alone.
func TestHalfClosedConnectionKeepsFloor(t *testing.T) {
	const floor = 1500 * time.Millisecond
	h, pool := siteAndPool(t, bundletest.Small(), Options{LoginFloor: floor})
	// A hash that takes a while to check (argon2id, 64 MiB), so that
	// the FIN lands while the hash runs.
	heavy := "$argon2id$v=19$m=65536,t=1,p=1$MDEyMzQ1Njc4OWFiY2RlZg$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err := pool.Exec(t.Context(), `update users set password_hash = $1, password_scheme = 'gobb' where id = 1`, heavy); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	// The sign-in page gives the guest cookie and token.
	resp, err := http.Get(srv.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	m := csrfField.FindSubmatch(page)
	if m == nil {
		t.Fatal("no csrf field")
	}
	var cookie string
	for _, c := range resp.Cookies() {
		if c.Name == csrfCookie {
			cookie = c.Name + "=" + c.Value
		}
	}
	form := url.Values{"name": {"alice"}, "password": {"wrong"}, "csrf": {string(m[1])}}.Encode()

	// The FIN has to land after the lookup and during the hash. Try a few times.
	for attempt := 1; attempt <= 4; attempt++ {
		conn, err := net.Dial("tcp", host)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(conn, "POST /login HTTP/1.1\r\nHost: %s\r\nOrigin: %s\r\nCookie: %s\r\n"+
			"Content-Type: application/x-www-form-urlencoded\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
			host, srv.URL, cookie, len(form), form)
		start := time.Now()
		time.Sleep(60 * time.Millisecond)
		if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
			t.Fatal(err)
		}
		br := bufio.NewReader(conn)
		if _, err := br.Peek(1); err != nil {
			t.Fatal(err)
		}
		first := time.Since(start)
		t.Logf("attempt %d: first byte after %v", attempt, first)
		status, _ := br.ReadString('\n')
		conn.Close()
		if !strings.Contains(status, " 200 ") {
			t.Logf("attempt %d: %s (the FIN came before the lookup); trying again", attempt, strings.TrimSpace(status))
			continue
		}
		if first < floor-20*time.Millisecond {
			t.Errorf("the first byte came after %v, before the floor %v", first, floor)
		}
		return
	}
	t.Fatal("no attempt got a sign-in answer")
}
