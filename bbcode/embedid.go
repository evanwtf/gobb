package bbcode

import (
	"net/url"
	"strings"
)

// maxEmbedInput bounds the work EmbedID does on one value.
const maxEmbedInput = 2048

// embedRule holds the rules for one embed tag.
type embedRule struct {
	bare func(string) bool                            // is the whole value a bare ID
	url  func(host, path string, q url.Values) string // the ID in a URL, or ""
}

var embedRules = map[string]embedRule{
	"youtube": {
		bare: func(s string) bool { return idChars(s, 11, 11, true) },
		url: func(host, path string, q url.Values) string {
			segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
			switch host {
			case "youtube.com":
				switch {
				case path == "/watch":
					return q.Get("v")
				case len(segs) == 2 && (segs[0] == "embed" || segs[0] == "shorts"):
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
		bare: func(s string) bool { return digitsOnly(s, 1, 12) },
		url: func(host, path string, _ url.Values) string {
			segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
			if host == "vimeo.com" && len(segs) == 1 {
				return segs[0]
			}
			return ""
		},
	},
	"tweet": {
		bare: func(s string) bool { return digitsOnly(s, 1, 20) },
		url: func(host, path string, _ url.Values) string {
			segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
			if (host == "twitter.com" || host == "x.com") && len(segs) == 3 &&
				segs[1] == "status" && idChars(segs[0], 1, 50, true) {
				return segs[2]
			}
			return ""
		},
	},
	"instagram": {
		bare: func(s string) bool { return idChars(s, 1, 64, true) },
		url: func(host, path string, _ url.Values) string {
			segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
			if host == "instagram.com" && len(segs) == 2 && segs[0] == "p" {
				return segs[1]
			}
			return ""
		},
	},
}

// EmbedID returns the canonical bare ID for an embedded medium. The tag is one
// of "youtube", "vimeo", "tweet" or "instagram". The value is a bare ID or a
// URL of the medium. EmbedID trims ASCII whitespace around the value. A URL
// uses http or https and has no user name or port. The host may start with
// "www.", the path may end with one slash, and the query and fragment are
// ignored, except for the "v" parameter of a YouTube watch URL. The scheme may
// be missing. EmbedID returns false when the tag is unknown or the value is not
// a bare ID or a URL of the medium.
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
	id := rule.url(host, path, q)
	if id == "" || !rule.bare(id) {
		return "", false
	}
	return id, true
}

// splitEmbedURL parses v as an http or https URL and returns the lower-case
// host without "www.", the escaped path without one trailing slash, and the
// query. A value with no scheme is read as https.
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
	host = strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	path = u.EscapedPath()
	if len(path) > 1 {
		path = strings.TrimSuffix(path, "/")
	}
	return host, path, u.Query(), host != ""
}

// idChars reports whether s has min to max bytes, each an ASCII letter or
// digit, or "_" or "-" when punct is true.
func idChars(s string, min, max int, punct bool) bool {
	if len(s) < min || len(s) > max {
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

// digitsOnly reports whether s has min to max bytes, all ASCII digits.
func digitsOnly(s string, min, max int) bool {
	if len(s) < min || len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
