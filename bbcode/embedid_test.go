package bbcode

import "testing"

func TestEmbedID(t *testing.T) {
	const yt = "dQw4w9WgXcQ"
	tests := []struct {
		tag, in string
		want    string
		ok      bool
	}{
		// youtube
		{"youtube", yt, yt, true},
		{"youtube", "https://www.youtube.com/watch?v=" + yt, yt, true},
		{"youtube", "http://youtube.com/watch?feature=x&v=" + yt + "&t=9", yt, true},
		{"youtube", "https://youtu.be/" + yt + "?si=abc", yt, true},
		{"youtube", "https://www.youtube.com/embed/" + yt, yt, true},
		{"youtube", "https://youtube.com/shorts/" + yt + "?feature=share", yt, true},
		{"youtube", "youtu.be/" + yt, yt, true},
		{"youtube", "  " + yt + "\n", yt, true},
		{"youtube", "too short", "", false},
		{"youtube", "dQw4w9WgXc", "", false},
		{"youtube", "dQw4w9WgXcQQ", "", false},
		{"youtube", "https://evil.example/watch?v=" + yt, "", false},
		{"youtube", "https://youtube.com.evil.example/watch?v=" + yt, "", false},
		{"youtube", "https://youtube.com/watch?v=bad id", "", false},
		{"youtube", "https://youtube.com/watch", "", false},
		{"youtube", "https://youtube.com/embed/" + yt + "/x", "", false},
		{"youtube", "https://youtube.com/embed%2F" + yt, "", false},
		{"youtube", "javascript://youtube.com/embed/" + yt, "", false},
		{"youtube", "ftp://youtube.com/embed/" + yt, "", false},
		{"youtube", "https://user@youtube.com/embed/" + yt, "", false},
		{"youtube", "https://youtube.com:8080/embed/" + yt, "", false},
		{"youtube", "", "", false},
		// vimeo
		{"vimeo", "76979871", "76979871", true},
		{"vimeo", "https://vimeo.com/76979871", "76979871", true},
		{"vimeo", "https://www.vimeo.com/76979871?x=1", "76979871", true},
		{"vimeo", "1234567890123", "", false},
		{"vimeo", "12ab", "", false},
		{"vimeo", "https://vimeo.com/channels/x", "", false},
		{"vimeo", "https://youtube.com/76979871", "", false},
		// tweet
		{"tweet", "1234567890123456789", "1234567890123456789", true},
		{"tweet", "https://twitter.com/someone/status/20", "20", true},
		{"tweet", "https://x.com/someone/status/20?s=46", "20", true},
		{"tweet", "http://www.twitter.com/some_one/status/20", "20", true},
		{"tweet", "123456789012345678901", "", false},
		{"tweet", "https://x.com/someone/likes/20", "", false},
		{"tweet", "https://x.com/some one/status/20", "", false},
		{"tweet", "https://nottwitter.com/someone/status/20", "", false},
		{"tweet", "abc", "", false},
		// instagram
		{"instagram", "Bk3_xY-9", "Bk3_xY-9", true},
		{"instagram", "https://www.instagram.com/p/Bk3_xY-9/", "Bk3_xY-9", true},
		{"instagram", "https://instagram.com/p/Bk3_xY-9?igsh=1", "Bk3_xY-9", true},
		{"instagram", "https://instagram.com/reel/Bk3_xY-9", "", false},
		{"instagram", "bad id!", "", false},
		{"instagram", "", "", false},
		// other
		{"nope", "dQw4w9WgXcQ", "", false},
	}
	for _, tc := range tests {
		got, ok := EmbedID(tc.tag, tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("EmbedID(%q, %q) = %q, %v; want %q, %v", tc.tag, tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestEmbedIDLongInput(t *testing.T) {
	long := make([]byte, 1<<20)
	for i := range long {
		long[i] = 'a'
	}
	for _, tag := range []string{"youtube", "vimeo", "tweet", "instagram"} {
		if id, ok := EmbedID(tag, string(long)); ok {
			t.Errorf("%s accepted a 1 MiB value as %d bytes", tag, len(id))
		}
	}
}
