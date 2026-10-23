package markup

import "testing"

func TestEmbedID(t *testing.T) {
	const (
		yt = "dQw4w9WgXcQ"
		sp = "4uLU6hMCjMI75M1A2tKUQC"
	)
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
		{"youtube", "https://m.youtube.com/watch?v=" + yt, yt, true},
		{"youtube", "https://music.youtube.com/watch?v=" + yt + "&list=x", yt, true},
		{"youtube", "https://www.youtube.com/live/" + yt + "?feature=share", yt, true},
		{"youtube", "https://youtube.com/live/" + yt + "/", yt, true},
		{"youtube", "https://m.youtube.com/live/" + yt, yt, true},
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
		{"youtube", "https://youtube.com/live/" + yt + "/x", "", false},
		{"youtube", "https://youtube.com/live", "", false},
		{"youtube", "https://www.m.youtube.com/watch?v=" + yt, "", false},
		{"youtube", "https://evil.youtube.com/watch?v=" + yt, "", false},
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
		{"tweet", "https://mobile.twitter.com/someone/status/20", "20", true},
		{"tweet", "https://mobile.x.com/someone/status/20", "", false},
		{"tweet", "https://www.mobile.twitter.com/someone/status/20", "", false},
		{"tweet", "123456789012345678901", "", false},
		{"tweet", "https://x.com/someone/likes/20", "", false},
		{"tweet", "https://x.com/some one/status/20", "", false},
		{"tweet", "https://nottwitter.com/someone/status/20", "", false},
		{"tweet", "abc", "", false},
		// instagram
		{"instagram", "Bk3_xY-9", "Bk3_xY-9", true},
		{"instagram", "https://www.instagram.com/p/Bk3_xY-9/", "Bk3_xY-9", true},
		{"instagram", "https://instagram.com/p/Bk3_xY-9?igsh=1", "Bk3_xY-9", true},
		{"instagram", "https://instagram.com/reel/Bk3_xY-9", "Bk3_xY-9", true},
		{"instagram", "https://www.instagram.com/reel/Bk3_xY-9/?igsh=1", "Bk3_xY-9", true},
		{"instagram", "https://instagram.com/tv/Bk3_xY-9", "Bk3_xY-9", true},
		{"instagram", "https://instagram.com/stories/Bk3_xY-9", "", false},
		{"instagram", "https://instagram.com/reel/Bk3_xY-9/x", "", false},
		{"instagram", "https://instagram.com/user/reel/Bk3_xY-9", "", false},
		{"instagram", "bad id!", "", false},
		{"instagram", "", "", false},
		// reddit
		{"reddit", "golang/abc123", "golang/abc123", true},
		{"reddit", "go/a", "go/a", true},
		{"reddit", "https://www.reddit.com/r/golang/comments/abc123", "golang/abc123", true},
		{"reddit", "https://reddit.com/r/golang/comments/abc123/", "golang/abc123", true},
		{"reddit", "https://reddit.com/r/golang/comments/abc123/a_post_title", "golang/abc123", true},
		{"reddit", "https://reddit.com/r/golang/comments/abc123/a_post_title/?utm_source=x", "golang/abc123", true},
		{"reddit", "https://old.reddit.com/r/Go_Lang/comments/abc123/slug/", "Go_Lang/abc123", true},
		{"reddit", "https://new.reddit.com/r/golang/comments/abc123", "golang/abc123", true},
		{"reddit", "https://www.old.reddit.com/r/golang/comments/abc123", "", false},
		{"reddit", "https://redd.it/abc123", "", false},
		{"reddit", "redd.it/abc123", "", false},
		{"reddit", "https://reddit.com/r/golang/comments/ABC123", "", false},
		{"reddit", "https://reddit.com/r/g/comments/abc123", "", false},
		{"reddit", "https://reddit.com/r/" + "aaaaaaaaaaaaaaaaaaaaaa" + "/comments/abc", "", false},
		{"reddit", "https://reddit.com/r/golang/comments/abcdefghijklm", "", false},
		{"reddit", "https://reddit.com/r/golang/comments", "", false},
		{"reddit", "https://reddit.com/r/golang/wiki/abc123", "", false},
		{"reddit", "https://reddit.com/u/golang/comments/abc123", "", false},
		{"reddit", "https://reddit.com/r/golang/comments/abc123/slug/extra", "", false},
		{"reddit", "golang/ABC", "", false},
		{"reddit", "g/abc", "", false},
		{"reddit", "golang", "", false},
		// tiktok
		{"tiktok", "7123456789012345678", "7123456789012345678", true},
		{"tiktok", "https://www.tiktok.com/@some.user/video/7123456789012345678", "7123456789012345678", true},
		{"tiktok", "https://tiktok.com/@user_1/video/7123456789012345678?lang=en", "7123456789012345678", true},
		{"tiktok", "https://vm.tiktok.com/ZMabc123/", "", false},
		{"tiktok", "vm.tiktok.com/ZMabc123", "", false},
		{"tiktok", "https://tiktok.com/user/video/7123456789012345678", "", false},
		{"tiktok", "https://tiktok.com/@user/photo/7123456789012345678", "", false},
		{"tiktok", "https://tiktok.com/@user/video/12a", "", false},
		{"tiktok", "https://tiktok.com/@/video/1", "", false},
		{"tiktok", "123456789012345678901", "", false},
		{"tiktok", "12a", "", false},
		// threads
		{"threads", "some.user/C-abc_123", "some.user/C-abc_123", true},
		{"threads", "https://www.threads.net/@some.user/post/C-abc_123", "some.user/C-abc_123", true},
		{"threads", "https://threads.com/@someone/post/Cabc123?xmt=x", "someone/Cabc123", true},
		{"threads", "https://threads.net/@someone/post/Cabc123/", "someone/Cabc123", true},
		{"threads", "https://threads.net/someone/post/Cabc123", "", false},
		{"threads", "https://threads.net/@someone/reply/Cabc123", "", false},
		{"threads", "https://threads.net/@someone", "", false},
		{"threads", "https://threads.org/@someone/post/Cabc123", "", false},
		{"threads", "https://threads.net/@bad user/post/Cabc123", "", false},
		{"threads", "someone", "", false},
		{"threads", "someone/bad post", "", false},
		// spotify
		{"spotify", "track/" + sp, "track/" + sp, true},
		{"spotify", "https://open.spotify.com/track/" + sp, "track/" + sp, true},
		{"spotify", "https://open.spotify.com/album/" + sp + "?si=abc", "album/" + sp, true},
		{"spotify", "https://open.spotify.com/playlist/" + sp, "playlist/" + sp, true},
		{"spotify", "https://open.spotify.com/episode/" + sp, "episode/" + sp, true},
		{"spotify", "https://open.spotify.com/show/" + sp + "/", "show/" + sp, true},
		{"spotify", "https://open.spotify.com/intl-de/track/" + sp, "track/" + sp, true},
		{"spotify", "https://open.spotify.com/intl-pt-br/track/" + sp + "?si=x", "track/" + sp, true},
		{"spotify", "https://spotify.link/abc123", "", false},
		{"spotify", "spotify.link/abc123", "", false},
		{"spotify", "https://open.spotify.com/artist/" + sp, "", false},
		{"spotify", "https://open.spotify.com/track/" + sp + "x", "", false},
		{"spotify", "https://open.spotify.com/track/short", "", false},
		{"spotify", "https://open.spotify.com/intl/track/" + sp, "", false},
		{"spotify", "https://open.spotify.com/intl-de/x/track/" + sp, "", false},
		{"spotify", "https://spotify.com/track/" + sp, "", false},
		{"spotify", "https://evil.example/track/" + sp, "", false},
		{"spotify", "https://www.open.spotify.com/track/" + sp, "", false},
		{"spotify", "artist/" + sp, "", false},
		{"spotify", "track/short", "", false},
		// soundcloud
		{"soundcloud", "some-user/a_track", "some-user/a_track", true},
		{"soundcloud", "some-user/sets/a_set", "some-user/sets/a_set", true},
		{"soundcloud", "https://soundcloud.com/some-user/a_track", "some-user/a_track", true},
		{"soundcloud", "https://www.soundcloud.com/some-user/a_track?si=x", "some-user/a_track", true},
		{"soundcloud", "https://soundcloud.com/some-user/sets/a_set/", "some-user/sets/a_set", true},
		{"soundcloud", "https://on.soundcloud.com/abc123", "", false},
		{"soundcloud", "on.soundcloud.com/abc123", "", false},
		{"soundcloud", "https://soundcloud.com/some-user", "", false},
		{"soundcloud", "https://soundcloud.com/some-user/sets", "", false},
		{"soundcloud", "https://soundcloud.com/Some-User/a_track", "", false},
		{"soundcloud", "https://soundcloud.com/some-user/a_track/extra", "", false},
		{"soundcloud", "https://soundcloud.com/some-user/sets/a_set/extra", "", false},
		{"soundcloud", "https://soundcloud.com/some-user/likes/a_set", "", false},
		{"soundcloud", "https://soundcloud.example/some-user/a_track", "", false},
		{"soundcloud", "some-user", "", false},
		{"soundcloud", "some-user/likes/a_set", "", false},
		{"soundcloud", "some user/a_track", "", false},
		// other
		{"nope", "dQw4w9WgXcQ", "", false},
		{"", "dQw4w9WgXcQ", "", false},
		{"youtu.be", "dQw4w9WgXcQ", "", false},
	}
	for _, tc := range tests {
		got, ok := EmbedID(tc.tag, tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("EmbedID(%q, %q) = %q, %v; want %q, %v", tc.tag, tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestEmbedIDResultIsCanonical(t *testing.T) {
	// Validate accepts an ID only when EmbedID returns it unchanged, so the
	// result of EmbedID must be a fixed point.
	for _, tc := range []struct{ tag, in string }{
		{"youtube", "https://m.youtube.com/live/dQw4w9WgXcQ"},
		{"reddit", "https://old.reddit.com/r/golang/comments/abc123/slug/"},
		{"tiktok", "https://tiktok.com/@a/video/1"},
		{"threads", "https://threads.com/@a.b/post/c-d"},
		{"spotify", "https://open.spotify.com/intl-de/album/4uLU6hMCjMI75M1A2tKUQC"},
		{"soundcloud", "https://soundcloud.com/a/sets/b"},
		{"instagram", "https://instagram.com/tv/abc"},
	} {
		id, ok := EmbedID(tc.tag, tc.in)
		if !ok {
			t.Fatalf("EmbedID(%q, %q) failed", tc.tag, tc.in)
		}
		again, ok := EmbedID(tc.tag, id)
		if !ok || again != id {
			t.Errorf("EmbedID(%q, %q) = %q, %v; want the same ID back", tc.tag, id, again, ok)
		}
	}
}

func TestEmbedIDLongInput(t *testing.T) {
	long := make([]byte, 1<<20)
	for i := range long {
		long[i] = 'a'
	}
	for _, tag := range []string{"youtube", "vimeo", "tweet", "instagram", "reddit", "tiktok", "threads", "spotify", "soundcloud"} {
		if id, ok := EmbedID(tag, string(long)); ok {
			t.Errorf("%s accepted a 1 MiB value as %d bytes", tag, len(id))
		}
	}
}
