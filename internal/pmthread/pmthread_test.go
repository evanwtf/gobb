package pmthread

import "testing"

func TestBaseSubject(t *testing.T) {
	for _, tc := range []struct {
		in, base string
		reply    bool
	}{
		{"Lunch", "lunch", false},
		{"Re: Lunch", "lunch", true},
		{"RE: re:Re:  Lunch ", "lunch", true},
		{"  re:LUNCH", "lunch", true},
		{"Reply soon", "reply soon", false},
		{"Re:", "", true},
	} {
		base, reply := baseSubject(tc.in)
		if base != tc.base || reply != tc.reply {
			t.Errorf("baseSubject(%q) = %q, %v; want %q, %v", tc.in, base, reply, tc.base, tc.reply)
		}
	}
}

func TestPeopleKey(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"1|2", "1,2"},
		{"2|1", "1,2"},
		{"3|1,2", "1,2,3"},
		{"1|1,2", "1,2"},
		{"name:ghost|", "name:ghost"},
	} {
		if got := peopleKey(tc.in); got != tc.want {
			t.Errorf("peopleKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
