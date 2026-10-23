package markup

import (
	"net/url"
	"strings"
)

// maxEmbedInput bounds the work EmbedID does on one value.
const maxEmbedInput = 2048

// embedRule holds the rules for one embed tag.
type embedRule struct {
	// hosts maps each accepted host to the canonical host that url sees.
	hosts map[string]string
	bare  func(string) bool                            // is the whole value a bare ID
	url   func(host, path string, q url.Values) string // the ID in a URL, or ""
}

// hostSet builds the host map for a rule. Each apex host is accepted with and
// without "www.". Each subdomain is accepted as written and maps to its apex.
func hostSet(apex []string, subs map[string]string) map[string]string {
	m := make(map[string]string, 2*len(apex)+len(subs))
	for _, h := range apex {
		m[h] = h
		m["www."+h] = h
	}
	for sub, canon := range subs {
		m[sub] = canon
	}
	return m
}

// segments splits an escaped URL path into its segments.
func segments(path string) []string {
	return strings.Split(strings.TrimPrefix(path, "/"), "/")
}

// idPair reports whether s is "a/b" where a passes isA and b passes isB.
func idPair(s string, isA, isB func(string) bool) bool {
	a, b, ok := strings.Cut(s, "/")
	return ok && isA(a) && isB(b)
}

func lowerDigits(s string, minLen, maxLen int) bool {
	if len(s) < minLen || len(s) > maxLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// slugChars reports whether s is 1 to maxLen bytes of lower-case letters,
// digits, "_" and "-".
func slugChars(s string, maxLen int) bool {
	if len(s) < 1 || len(s) > maxLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// userChars reports whether s is 1 to maxLen bytes of letters, digits, "_" and
// ".".
func userChars(s string, maxLen int) bool {
	if len(s) < 1 || len(s) > maxLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

// isSoundcloudID accepts "user/track" or "user/sets/set".
func isSoundcloudID(s string) bool {
	segs := strings.Split(s, "/")
	switch len(segs) {
	case 2:
		return slugChars(segs[0], 100) && slugChars(segs[1], 100) && segs[1] != "sets"
	case 3:
		return slugChars(segs[0], 100) && segs[1] == "sets" && slugChars(segs[2], 100)
	}
	return false
}

// spotifyTypes are the content types that have a Spotify player.
var spotifyTypes = map[string]bool{"track": true, "album": true, "playlist": true, "episode": true, "show": true}

func isSpotifyID(s string) bool {
	return idPair(s, func(a string) bool { return spotifyTypes[a] }, func(b string) bool { return idChars(b, 22, 22, false) })
}

// isLocale accepts the "intl-xx" segment that Spotify puts before the type.
func isLocale(s string) bool {
	rest, ok := strings.CutPrefix(s, "intl-")
	if !ok || len(rest) < 2 || len(rest) > 8 {
		return false
	}
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-') {
			return false
		}
	}
	return true
}

var embedRules = map[string]embedRule{
	"youtube": {
		hosts: hostSet([]string{"youtube.com", "youtu.be"}, map[string]string{
			"m.youtube.com":     "youtube.com",
			"music.youtube.com": "youtube.com",
		}),
		bare: func(s string) bool { return idChars(s, 11, 11, true) },
		url: func(host, path string, q url.Values) string {
			segs := segments(path)
			switch host {
			case "youtube.com":
				switch {
				case path == "/watch":
					return q.Get("v")
				case len(segs) == 2 && (segs[0] == "embed" || segs[0] == "shorts" || segs[0] == "live"):
					return segs[1]
				}
			case "youtu.be":
				if len(segs) == 1 {
					return segs[0]
				}
			}
			return ""
		},
	},
	"vimeo": {
		hosts: hostSet([]string{"vimeo.com"}, nil),
		bare:  func(s string) bool { return digitsOnly(s, 1, 12) },
		url: func(_, path string, _ url.Values) string {
			if segs := segments(path); len(segs) == 1 {
				return segs[0]
			}
			return ""
		},
	},
	"tweet": {
		hosts: hostSet([]string{"twitter.com", "x.com"}, map[string]string{"mobile.twitter.com": "twitter.com"}),
		bare:  func(s string) bool { return digitsOnly(s, 1, 20) },
		url: func(_, path string, _ url.Values) string {
			if segs := segments(path); len(segs) == 3 && segs[1] == "status" && idChars(segs[0], 1, 50, true) {
				return segs[2]
			}
			return ""
		},
	},
	"instagram": {
		hosts: hostSet([]string{"instagram.com"}, nil),
		bare:  func(s string) bool { return idChars(s, 1, 64, true) },
		url: func(_, path string, _ url.Values) string {
			if segs := segments(path); len(segs) == 2 && (segs[0] == "p" || segs[0] == "reel" || segs[0] == "tv") {
				return segs[1]
			}
			return ""
		},
	},
	"reddit": {
		hosts: hostSet([]string{"reddit.com"}, map[string]string{"old.reddit.com": "reddit.com", "new.reddit.com": "reddit.com"}),
		bare: func(s string) bool {
			return idPair(s, func(a string) bool { return idChars(a, 2, 21, true) && !strings.Contains(a, "-") },
				func(b string) bool { return lowerDigits(b, 1, 12) })
		},
		url: func(_, path string, _ url.Values) string {
			segs := segments(path)
			if (len(segs) == 4 || len(segs) == 5) && segs[0] == "r" && segs[2] == "comments" &&
				(len(segs) == 4 || idChars(segs[4], 1, 300, true)) {
				return segs[1] + "/" + segs[3]
			}
			return ""
		},
	},
	"tiktok": {
		hosts: hostSet([]string{"tiktok.com"}, nil),
		bare:  func(s string) bool { return digitsOnly(s, 1, 20) },
		url: func(_, path string, _ url.Values) string {
			segs := segments(path)
			if len(segs) == 3 && strings.HasPrefix(segs[0], "@") && userChars(segs[0][1:], 64) && segs[1] == "video" {
				return segs[2]
			}
			return ""
		},
	},
	"threads": {
		hosts: hostSet([]string{"threads.net", "threads.com"}, nil),
		bare: func(s string) bool {
			return idPair(s, func(a string) bool { return userChars(a, 30) }, func(b string) bool { return idChars(b, 1, 64, true) })
		},
		url: func(_, path string, _ url.Values) string {
			segs := segments(path)
			if len(segs) == 3 && strings.HasPrefix(segs[0], "@") && userChars(segs[0][1:], 30) && segs[1] == "post" {
				return segs[0][1:] + "/" + segs[2]
			}
			return ""
		},
	},
	"spotify": {
		hosts: map[string]string{"open.spotify.com": "open.spotify.com"},
		bare:  isSpotifyID,
		url: func(_, path string, _ url.Values) string {
			segs := segments(path)
			if len(segs) == 3 && isLocale(segs[0]) {
				segs = segs[1:]
			}
			if len(segs) == 2 {
				return segs[0] + "/" + segs[1]
			}
			return ""
		},
	},
	"soundcloud": {
		hosts: hostSet([]string{"soundcloud.com"}, nil),
		bare:  isSoundcloudID,
		url: func(_, path string, _ url.Values) string {
			if segs := segments(path); len(segs) == 2 || len(segs) == 3 {
				return strings.Join(segs, "/")
			}
			return ""
		},
	},
}

// IsEmbed reports whether name is the element name of an embed.
func IsEmbed(name string) bool {
	_, ok := embedRules[name]
	return ok
}

// EmbedID returns the canonical bare ID for an embedded medium. The tag is the
// name of an embed element: "youtube", "vimeo", "tweet", "instagram", "reddit",
// "tiktok", "threads", "spotify" or "soundcloud". The value is a bare ID or a
// URL of the medium. EmbedID trims ASCII whitespace around the value. A URL
// uses http or https and has no user name or port. The host may start with
// "www." or be one of the listed subdomains of the medium, the path may end with
// one slash, and the query and fragment are ignored, except for the "v"
// parameter of a YouTube watch URL. The scheme may be missing.
//
// Short links need a server lookup, so EmbedID returns false for them. That
// covers redd.it, vm.tiktok.com, spotify.link and on.soundcloud.com. The
// youtu.be form is a plain ID in the path, so it works. EmbedID returns false
// when the tag is unknown or the value is not a bare ID or a URL of the medium.
func EmbedID(tag, value string) (string, bool) {
	rule, ok := embedRules[tag]
	if !ok {
		return "", false
	}
	value = strings.Trim(value, " \t\r\n\f\v")
	if value == "" || len(value) > maxEmbedInput {
		return "", false
	}
	if rule.bare(value) {
		return value, true
	}
	host, path, q, ok := splitEmbedURL(value)
	if !ok {
		return "", false
	}
	canon, ok := rule.hosts[host]
	if !ok {
		return "", false
	}
	id := rule.url(canon, path, q)
	if id == "" || !rule.bare(id) {
		return "", false
	}
	return id, true
}

// splitEmbedURL parses v as an http or https URL and returns the lower-case
// host, the escaped path without one trailing slash, and the query. A value with no scheme is read as https.
func splitEmbedURL(v string) (host, path string, q url.Values, ok bool) {
	if i := strings.Index(v, "://"); i < 0 || strings.ContainsAny(v[:i], "/?#") {
		v = "https://" + v
	}
	u, err := url.Parse(v)
	if err != nil {
		return "", "", nil, false
	}
	if s := strings.ToLower(u.Scheme); s != "http" && s != "https" {
		return "", "", nil, false
	}
	if u.User != nil || u.Port() != "" {
		return "", "", nil, false
	}
	host = strings.ToLower(u.Hostname())
	path = u.EscapedPath()
	if len(path) > 1 {
		path = strings.TrimSuffix(path, "/")
	}
	return host, path, u.Query(), host != ""
}

// idChars reports whether s has minLen to maxLen bytes, each an ASCII letter or
// digit, or "_" or "-" when punct is true.
func idChars(s string, minLen, maxLen int, punct bool) bool {
	if len(s) < minLen || len(s) > maxLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case punct && (c == '_' || c == '-'):
		default:
			return false
		}
	}
	return true
}

// digitsOnly reports whether s has minLen to maxLen bytes, all ASCII digits.
func digitsOnly(s string, minLen, maxLen int) bool {
	if len(s) < minLen || len(s) > maxLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
