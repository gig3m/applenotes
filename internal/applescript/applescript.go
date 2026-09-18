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
)

// ErrNotPermitted is returned when macOS has not granted Automation access to
// Notes.app for this binary.
var ErrNotPermitted = errors.New("applescript: not permitted to control Notes (grant Automation access, or run from a logged-in GUI session)")

// Timeout bounds a single Apple Event. They are slow, and a blocked consent
// prompt otherwise hangs forever with no output.
var Timeout = 60 * time.Second

// Run executes src with args available to its `on run argv` handler and returns
// trimmed stdout.
func Run(ctx context.Context, src string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()

	// "-" reads the script from stdin; everything after it is argv.
	cmd := exec.CommandContext(ctx, "osascript", append([]string{"-"}, args...)...)
	cmd.Stdin = strings.NewReader(src)

	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()

	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("applescript: timed out after %s (a consent dialog may be waiting on the Mac's screen)", Timeout)
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
