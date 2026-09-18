// Package edit opens a note in an editor and writes back what comes out.
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
	"path/filepath"
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

// editorFor decides what to open the buffer with.
//
// $EDITOR is not automatically usable here. `notes edit` runs in a terminal and
// has to block until the user is done, but on a desktop $EDITOR is often a
// launcher that hands the file to a GUI window and returns immediately -- at
// which point the buffer is read back unchanged and the edit is lost. Telling
// the user to go and reconfigure their system for one command is not a fix, so
// this picks something that works and says what it picked.
//
// The chosen command is returned along with a note to show the user, empty when
// the choice needs no explanation.
func editorFor(env func(string) string, look func(string) (string, error)) (argv []string, note string, err error) {
	// NOTES_EDITOR is the escape hatch: set it for this tool alone, without
	// touching $EDITOR for everything else.
	for _, key := range []string{"NOTES_EDITOR", "VISUAL", "EDITOR"} {
		ed := strings.TrimSpace(env(key))
		if ed == "" {
			continue
		}
		parts := strings.Fields(ed)
		if key == "NOTES_EDITOR" || blocks(parts) {
			return parts, "", nil
		}
		// Configured, but it will not wait. Fall back rather than lose the edit.
		if term := firstOnPath(look); term != "" {
			// Deliberately not suggesting a wait flag for their command: it may
			// not have one, and advice that does not work is worse than none.
			return []string{term}, fmt.Sprintf(
				"$%s is %q, which does not wait for you to finish, so this edit uses %s instead.\n"+
					"       Set NOTES_EDITOR to choose for yourself, for example\n"+
					"         NOTES_EDITOR=nvim   NOTES_EDITOR='code -w'   NOTES_EDITOR='zed --wait'",
				key, ed, term), nil
		}
		return nil, "", fmt.Errorf(
			"$%s is %q, which returns before you have finished editing, and no terminal\n"+
				"       editor was found to use instead. Set one: NOTES_EDITOR=nano", key, ed)
	}
	if term := firstOnPath(look); term != "" {
		return []string{term}, "", nil
	}
	return nil, "", fmt.Errorf("no editor found; set NOTES_EDITOR (for example NOTES_EDITOR=nano)")
}

// terminalEditors are tried in order when nothing usable is configured.
var terminalEditors = []string{"nvim", "vim", "helix", "hx", "kak", "micro", "nano", "vis", "vi"}

// blocks reports whether a command waits for the editor to be closed. Either it
// is an editor that runs inside this terminal, or it is a GUI editor carrying
// the flag that makes it wait.
func blocks(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	name := filepath.Base(argv[0])
	for _, flag := range argv[1:] {
		switch flag {
		case "-w", "--wait", "-W", "--block":
			return true
		}
	}
	// emacs and emacsclient open a window unless told to stay in the terminal.
	if name == "emacs" || name == "emacsclient" {
		for _, flag := range argv[1:] {
			if flag == "-nw" || flag == "-t" || flag == "--tty" || flag == "-tty" {
				return true
			}
		}
		return false
	}
	for _, t := range terminalEditors {
		if name == t {
			return true
		}
	}
	// Unknown: assume it is a launcher. Guessing wrong the other way silently
	// discards what the user typed, which is the worse of the two mistakes.
	return false
}

func firstOnPath(look func(string) (string, error)) string {
	for _, ed := range terminalEditors {
		if _, err := look(ed); err == nil {
			return ed
		}
	}
	return ""
}

func runEditor(path string) error {
	argv, note, err := editorFor(os.Getenv, exec.LookPath)
	if err != nil {
		return err
	}
	if note != "" {
		fmt.Fprintln(os.Stderr, "notes: "+note)
	}
	cmd := exec.Command(argv[0], append(argv[1:], path)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
