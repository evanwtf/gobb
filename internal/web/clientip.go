package web

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ClientIP returns the address of the client that sent r.
//
// When the peer (RemoteAddr) is not in trusted, the peer is the client, and
// X-Forwarded-For is ignored: anyone can send that header. When the peer is
// trusted, ClientIP walks X-Forwarded-For from the right, skips the hops that
// are trusted too, and returns the first address that is not. Each proxy appends
// the address it saw, so the right end is the part that a trusted proxy wrote.
//
// An entry that is not an address ends the walk, and the peer is the result.
// So is a list in which every hop is trusted. An IPv4-mapped address becomes the
// plain IPv4 address, and a zone is dropped. The zero Addr means the peer
// address could not be read.
func ClientIP(r *http.Request, trusted []netip.Prefix) netip.Addr {
	peer := parseAddr(r.RemoteAddr)
	if !peer.IsValid() || !isTrusted(peer, trusted) {
		return peer
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop := strings.TrimSpace(hops[i])
		addr := parseAddr(hop)
		if !addr.IsValid() {
			return peer
		}
		if !isTrusted(addr, trusted) {
			return addr
		}
	}
	return peer
}

// parseAddr reads an address, or an address with a port, in the plain form.
// It returns the zero Addr for anything else.
func parseAddr(s string) netip.Addr {
	s = strings.TrimSpace(s)
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap().WithZone("")
}

func isTrusted(a netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
