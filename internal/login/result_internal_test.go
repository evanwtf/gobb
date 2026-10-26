package login

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// signinResult names every outcome of a check, wrapped or not.
func TestSigninResult(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{nil, "ok"},
		{ErrWrong, "wrong"},
		{fmt.Errorf("x: %w", ErrWrong), "wrong"},
		{ErrAmbiguous, "ambiguous"},
		{&LimitedError{}, "limited"},
		{&BannedError{Reason: "r"}, "banned"},
		{ErrInactive, "inactive"},
		{ErrBusy, "busy"},
		{context.Canceled, "error"},
		{errors.New("db down"), "error"},
	} {
		if got := signinResult(c.err); got != c.want {
			t.Errorf("signinResult(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}
