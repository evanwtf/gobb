package acl

// Scope says where an option applies.
type Scope uint8

const (
	// Forum options apply per forum.
	Forum Scope = 1
	// Board options apply board-wide. A grant on a forum does not change them.
	Board Scope = 2
	// Moderator options exist at both levels. The result in a forum is the
	// board-wide result OR the forum result.
	Moderator Scope = 3
)

// Option names one right and says where it applies.
type Option struct {
	Name  string
	Scope Scope
}

// Options is the option registry (spec section 9.1). Names are stored in the
// database; do not rename one.
var Options = []Option{
	// Forum options.
	{"forum.list", Forum},
	{"forum.read", Forum},
	{"forum.search", Forum},
	{"topic.start", Forum},
	{"topic.reply", Forum},
	{"topic.sticky", Forum},
	{"topic.announce", Forum},
	{"topic.global", Forum},
	{"topic.lock_own", Forum},
	{"topic.watch", Forum},
	{"post.edit_own", Forum},
	{"post.delete_own", Forum},
	{"post.bbcode", Forum},
	{"post.smilies", Forum},
	{"post.images", Forum},
	{"post.signatures", Forum},
	{"post.no_approval", Forum},
	{"post.counts", Forum},
	{"attach.upload", Forum},
	{"attach.download", Forum},
	{"poll.start", Forum},
	{"poll.vote", Forum},
	{"poll.change_vote", Forum},

	// Moderator options.
	{"mod.edit", Moderator},
	{"mod.delete", Moderator},
	{"mod.approve", Moderator},
	{"mod.lock", Moderator},
	{"mod.move", Moderator},
	{"mod.view_ip", Moderator},

	// Board options.
	{"mod.ban", Board},
	{"pm.send", Board},
	{"pm.read", Board},
	{"pm.send_many", Board},
	{"pm.bbcode", Board},
	{"pm.smilies", Board},
	{"pm.images", Board},
	{"pm.edit", Board},
	{"pm.delete", Board},
	{"account.signature", Board},
	{"account.avatar", Board},
	{"account.profile", Board},
	{"account.email", Board},
	{"account.password", Board},
	{"account.attach", Board},
	{"account.download", Board},
	{"account.search", Board},
	{"account.view_profiles", Board},
	{"admin", Board},
}

var byName = func() map[string]Option {
	m := make(map[string]Option, len(Options))
	for _, o := range Options {
		m[o.Name] = o
	}
	return m
}()

// Lookup returns the registry entry for name.
func Lookup(name string) (Option, bool) {
	o, ok := byName[name]
	return o, ok
}
