package web

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.231.0.1/32"), netip.MustParsePrefix("10.231.0.2/32")}
	for _, c := range []struct {
		name, remote string
		xff          []string
		want         string
	}{
		{"walks from the right and skips trusted hops", "10.231.0.1:5000", []string{"10.9.9.9, 10.1.1.1, 10.231.0.2"}, "10.1.1.1"},
		{"an untrusted peer is the client", "10.7.7.7:5000", []string{"10.1.1.1"}, "10.7.7.7"},
		{"a bad entry gives the peer", "10.231.0.1:5000", []string{"garbage"}, "10.231.0.1"},
		{"a bad entry beyond a good one ends the walk", "10.231.0.1:5000", []string{"garbage, 10.231.0.2"}, "10.231.0.1"},
		{"a mapped address is unmapped", "10.231.0.1:5000", []string{"::ffff:10.1.1.1"}, "10.1.1.1"},
		{"a mapped peer is unmapped and trusted", "[::ffff:10.231.0.1]:5000", []string{"10.1.1.1"}, "10.1.1.1"},
		{"no header gives the peer", "10.231.0.1:5000", nil, "10.231.0.1"},
		{"only trusted hops give the peer", "10.231.0.1:5000", []string{"10.231.0.2"}, "10.231.0.1"},
		{"two header lines count as one list", "10.231.0.1:5000", []string{"10.9.9.9", "10.1.1.1, 10.231.0.2"}, "10.1.1.1"},
		{"an empty entry ends the walk", "10.231.0.1:5000", []string{"10.1.1.1,, 10.231.0.2"}, "10.231.0.1"},
		{"an IPv6 client", "10.231.0.1:5000", []string{"2001:db8::7"}, "2001:db8::7"},
		{"a zone is dropped", "10.231.0.1:5000", []string{"fe80::1%eth0"}, "fe80::1"},
		{"a peer with no port", "10.7.7.7", nil, "10.7.7.7"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = c.remote
			for _, v := range c.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			got := ClientIP(r, trusted)
			if got != netip.MustParseAddr(c.want) {
				t.Errorf("ClientIP = %v, want %v", got, c.want)
			}
		})
	}
}

// A request with no usable peer address gives the zero address.
func TestClientIPUnparseable(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "@"
	if got := ClientIP(r, nil); got.IsValid() {
		t.Errorf("ClientIP = %v, want the zero address", got)
	}
}
