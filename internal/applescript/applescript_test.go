package applescript

import (
	"context"
	"errors"
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

// osascript frames every failure with byte offsets into a script the user never
// wrote. Left in, they are the first thing shown -- and in a one-line status bar
// they are the only thing shown, pushing out the sentence that says what went
// wrong.
func TestScriptErrorsAreReadable(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"97:139: execution error: Notes got an error: Can’t modify a note in Recently Deleted. (-10000)",
			"Notes got an error: Can’t modify a note in Recently Deleted. (-10000)"},
		{"0:0: execution error: Notes got an error: AppleEvent timed out. (-1712)",
			"Notes got an error: AppleEvent timed out. (-1712)"},
		// Nothing to strip: left alone rather than mangled.
		{"some other failure", "some other failure"},
		{"", ""},
	} {
		if got := cleanScriptError(tc.raw); got != strings.TrimSuffix(tc.want, ".") {
			t.Errorf("cleanScriptError(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// A note in Recently Deleted is a case a client cannot avoid: the database can
// report it as ordinary for a minute after it is moved, so the write is tried
// and fails. It gets its own error rather than raw script output.
func TestTrashedNoteGetsItsOwnError(t *testing.T) {
	if !errors.Is(ErrInTrash, ErrInTrash) {
		t.Fatal("sentinel does not match itself")
	}
	if !strings.Contains(ErrInTrash.Error(), "Recently Deleted") {
		t.Errorf("the error does not say what is wrong: %v", ErrInTrash)
	}
}
