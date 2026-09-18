// Package applescript drives Notes.app through Apple Events.
//
// Scripts are fixed text and every piece of data travels in argv. Interpolating
// note content into an AppleScript literal would let a quote or backslash in a
// note break out of the string and run as code, and note content is exactly the
// thing this package does not control.
package applescript

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrNotPermitted is returned when macOS has not granted Automation access to
// Notes.app for this binary.
var ErrNotPermitted = errors.New("applescript: not permitted to control Notes (grant Automation access, or run from a logged-in GUI session)")

// Timeout bounds a single Apple Event. They are slow, and a blocked consent
// prompt otherwise hangs forever with no output.
var Timeout = 60 * time.Second

// MaxArgBytes bounds the total size of argv. macOS caps argv plus environment
// at kern.argmax, 1 MiB by default; exceeding it fails with an opaque
// "argument list too long".
const MaxArgBytes = 512 << 10

// Run executes src with args available to its `on run argv` handler and returns
// trimmed stdout.
//
// A timeout does not mean nothing was written. The Apple Event has already been
// delivered to Notes.app, which may complete it after osascript is killed, so a
// caller cannot treat a timeout as "no change".
func Run(ctx context.Context, src string, args ...string) (string, error) {
	total := 0
	for i, a := range args {
		if strings.ContainsRune(a, 0) {
			return "", fmt.Errorf("applescript: argument %d contains a NUL byte", i+1)
		}
		if !utf8.ValidString(a) {
			return "", fmt.Errorf("applescript: argument %d is not valid UTF-8", i+1)
		}
		total += len(a)
	}
	if total > MaxArgBytes {
		return "", fmt.Errorf("applescript: %d bytes of arguments exceeds the %d byte limit", total, MaxArgBytes)
	}

	deadlineCtx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	ctx = deadlineCtx

	// "-" reads the script from stdin; everything after it is argv.
	cmd := exec.CommandContext(ctx, "osascript", append([]string{"-"}, args...)...)
	cmd.Stdin = strings.NewReader(src)

	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()

	if err != nil && deadlineCtx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("applescript: timed out after %s; the change may still have been applied, since Notes.app has already received the event (a consent dialog may also be waiting on the Mac's screen)", Timeout)
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		// -1743 is errAEEventNotPermitted; -600 is "application isn't running"
		// when the event cannot be delivered at all.
		if strings.Contains(msg, "-1743") || strings.Contains(msg, "Not authorized") {
			return "", ErrNotPermitted
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("applescript: %s", msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// Available reports whether Apple Events to Notes work from this process.
func Available(ctx context.Context) error {
	_, err := Run(ctx, `on run argv
	tell application "Notes" to return name of default account
end run`)
	return err
}
