// Package edit opens a note in $EDITOR and writes back what comes out.
//
// There is no built-in editor here on purpose. Editing prose is a solved
// problem and the user already has an answer to it; what this package owes them
// is to be careful about when it writes back, because a rewrite is lossy for
// anything Markdown cannot express and there is no undo.
package edit

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Store is the part of a notes source this package needs. Both the local store
// and the HTTP client satisfy it.
type Store interface {
	Fetch(ctx context.Context, uuid string) (string, error)
	Write(ctx context.Context, uuid, markdown string, force bool) ([]string, error)
}

type Editor struct {
	Store Store

	// Run invokes the editor on a path. Replaced in tests; otherwise $EDITOR.
	Run func(path string) error

	// Force passes through to the write, for a note whose content the rewrite
	// would destroy.
	Force bool

	// OnDegrade is called with the formatting the write flattened.
	OnDegrade func(features []string)
}

// Edit fetches a note, opens it, and writes back only if it should. It reports
// whether anything was written, so a caller does not tell the user their note
// was saved when the editor changed nothing.
func (e *Editor) Edit(ctx context.Context, uuid string) (wrote bool, err error) {
	// Fetch first: editing a note we could not read would replace it with
	// whatever the editor started from.
	body, err := e.Store.Fetch(ctx, uuid)
	if err != nil {
		return false, fmt.Errorf("reading the note: %w", err)
	}

	f, err := os.CreateTemp("", "note-*.md")
	if err != nil {
		return false, err
	}
	path := f.Name()
	// A note can hold anything, so the buffer is readable only by this user.
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(path)
		return false, err
	}
	if _, err := f.WriteString(body); err != nil {
		f.Close()
		os.Remove(path)
		return false, err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return false, err
	}

	run := e.Run
	if run == nil {
		run = runEditor
	}
	if err := run(path); err != nil {
		// A crashed editor must not be read as "the user emptied the note".
		os.Remove(path)
		return false, fmt.Errorf("editor: %w", err)
	}

	edited, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	if string(edited) == body {
		os.Remove(path)
		return false, nil // nothing changed; a rewrite costs formatting for nothing
	}
	if len(bytes.TrimSpace(edited)) == 0 {
		os.Remove(path)
		return false, fmt.Errorf("refusing to empty the note; nothing was written")
	}

	degraded, err := e.Store.Write(ctx, uuid, string(edited), e.Force)
	if err != nil {
		// The buffer stays: the work in it is the user's, and the write may be
		// retryable with force.
		return false, fmt.Errorf("writing the note (your edit is kept in %s): %w", path, err)
	}
	os.Remove(path)
	if e.OnDegrade != nil && len(degraded) > 0 {
		e.OnDegrade(degraded)
	}
	return true, nil
}

func runEditor(path string) error {
	ed := os.Getenv("VISUAL")
	if ed == "" {
		ed = os.Getenv("EDITOR")
	}
	if ed == "" {
		ed = "vi"
	}
	// Split on spaces so EDITOR="code -w" works.
	parts := strings.Fields(ed)
	cmd := exec.Command(parts[0], append(parts[1:], path)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
