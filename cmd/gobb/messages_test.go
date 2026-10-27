package main

import (
	"strings"
	"testing"
)

func TestMessagesThread(t *testing.T) {
	pool := userBoard(t)
	conv := func() int {
		return count(t, pool, `select conversation_id::int from pms where id = 2`)
	}
	code, out, errOut := runUser(t, "messages", "thread", "-dry-run")
	if code != 0 || out != "" || !strings.Contains(errOut, "would_join=1") || conv() != 2 {
		t.Fatalf("dry run = %d %q %q, conversation %d", code, out, errOut, conv())
	}
	code, _, errOut = runUser(t, "messages", "thread")
	if code != 0 || !strings.Contains(errOut, "joined=1") || conv() != 1 {
		t.Fatalf("thread = %d %q, conversation %d", code, errOut, conv())
	}
	if code, _, errOut = runUser(t, "messages", "thread"); code != 0 || !strings.Contains(errOut, "joined=0") {
		t.Errorf("second thread = %d %q", code, errOut)
	}
}

func TestMessagesUsage(t *testing.T) {
	for _, args := range [][]string{{"messages"}, {"messages", "x"}, {"messages", "thread", "extra"}, {"messages", "thread", "-bad"}} {
		if code, _, _ := runUser(t, args...); code != 2 {
			t.Errorf("%v = %d, want 2", args, code)
		}
	}
}
