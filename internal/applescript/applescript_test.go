package applescript

import (
	"context"
	"strings"
	"testing"
)

// Arguments carrying a NUL or invalid UTF-8 would fail inside os/exec with an
// opaque "invalid argument", so they are rejected with a real message first.
func TestRunRejectsUnusableArguments(t *testing.T) {
	for _, tc := range []struct{ name, arg, want string }{
		{"nul", "a\x00b", "NUL"},
		{"invalid utf-8", string([]byte{0xff, 0xfe}), "UTF-8"},
		{"too large", strings.Repeat("x", MaxArgBytes+1), "exceeds"},
	} {
		_, err := Run(context.Background(), "on run argv\nreturn\nend run", tc.arg)
		if err == nil {
			t.Errorf("%s: expected an error", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %q, want it to mention %q", tc.name, err, tc.want)
		}
	}
}

// The size limit is on the total, not on any one argument.
func TestRunSizeLimitIsCumulative(t *testing.T) {
	half := strings.Repeat("x", MaxArgBytes/2+1)
	if _, err := Run(context.Background(), "on run argv\nreturn\nend run", half, half); err == nil ||
		!strings.Contains(err.Error(), "exceeds") {
		t.Errorf("got %v, want a size error", err)
	}
}
