package negcheck

import "testing"

func TestSkipped(t *testing.T) {
	t.Skip("negative check: CI must fail on a skipped test")
}
