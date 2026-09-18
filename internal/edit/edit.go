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
	"io"
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
		run = Runner(os.Stderr)
	}
	if err := run(path); err != nil {
		// Nothing is written -- a crashed editor must not be read as "the user
		// emptied the note" -- but the buffer is kept and named. The user may
		// have saved before it died, and this package's whole argument is that
		// losing their writing is the worse mistake.
		return false, fmt.Errorf("editor (your buffer is kept in %s): %w", path, err)
	}

	edited, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("reading back your edit (it is kept in %s): %w", path, err)
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
	var tried []string
	for _, key := range []string{"NOTES_EDITOR", "VISUAL", "EDITOR"} {
		ed := strings.TrimSpace(env(key))
		if ed == "" {
			continue
		}
		parts := splitEditor(ed, look)
		if len(parts) == 0 {
			// Deliberately redundant with the TrimSpace above: an empty argv
			// reaches exec as argv[0] on a zero-length slice, which panics.
			// Neither check alone is load-bearing; do not remove either.
			continue
		}
		// NOTES_EDITOR is the escape hatch: set it for this tool alone, without
		// touching $EDITOR for everything else. It is obeyed exactly.
		if key == "NOTES_EDITOR" {
			return parts, "", nil
		}
		if blocks(parts) {
			return parts, "", nil
		}
		// Keep looking rather than giving up here. VISUAL naming a GUI editor
		// and EDITOR naming a terminal one is the convention these variables
		// exist for, so a launcher in VISUAL must not hide a usable EDITOR.
		tried = append(tried, fmt.Sprintf("$%s (%s)", key, ed))
	}

	if term := firstOnPath(look); term != "" {
		if len(tried) == 0 {
			return []string{term}, "", nil
		}
		// Carefully not "does not wait": for an unrecognised command that is a
		// guess, and several editors that do wait land here. Nor is a wait flag
		// suggested for their command, since it may not have one.
		return []string{term}, fmt.Sprintf(
			"cannot confirm %s waits for you to finish, so this edit uses %s instead.\n"+
				"       Set NOTES_EDITOR to choose for yourself, for example\n"+
				"         NOTES_EDITOR=nvim   NOTES_EDITOR='code -w'   NOTES_EDITOR='zed --wait'",
			strings.Join(tried, " or "), term), nil
	}
	if len(tried) > 0 {
		return nil, "", fmt.Errorf(
			"cannot confirm %s waits for you to finish, and no terminal editor was\n"+
				"       found to use instead. Set one: NOTES_EDITOR=nano", strings.Join(tried, " or "))
	}
	return nil, "", fmt.Errorf("no editor found; set NOTES_EDITOR (for example NOTES_EDITOR=nano)")
}

// splitEditor turns a setting into argv. A bare path is not split, so an editor
// living under a directory with a space in it can still be named; anything else
// is split on spaces so "code -w" works.
func splitEditor(ed string, look func(string) (string, error)) []string {
	if _, err := look(ed); err == nil {
		return []string{ed}
	}
	return strings.Fields(ed)
}

// fallbackOrder is what to reach for when nothing usable is configured, best
// first. ed is last and is genuinely unpleasant, but it is present on every
// unix and beats refusing to edit at all.
var fallbackOrder = []string{"nvim", "vim", "helix", "hx", "kak", "micro", "nano", "vis", "vi", "ed"}

// terminalEditors run inside the terminal they are started from, so the command
// does not return until the user is finished.
var terminalEditors = map[string]bool{
	"nano": true, "pico": true, "micro": true, "helix": true, "hx": true,
	"kak": true, "vis": true, "ed": true, "joe": true, "jed": true,
	"mg": true, "ne": true, "mcedit": true, "jmacs": true, "zile": true,
	"dte": true, "tilde": true, "nvi": true,
}

// vimFamily needs its own rule: -w and -W here mean "record keystrokes to this
// file" and take an argument, so the generic wait-flag test would read a vim
// script log as a promise to wait.
var vimFamily = map[string]bool{
	"vi": true, "vim": true, "nvim": true, "view": true, "vimdiff": true,
	"ex": true, "gvim": true, "mvim": true, "gview": true, "evim": true,
}

// guiVim forks into a window unless given -f/--nofork. "vim -g" is the same
// program by another name.
var guiVim = map[string]bool{"gvim": true, "mvim": true, "gview": true, "evim": true}

// blocks reports whether a command waits for the user to finish editing.
func blocks(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	name := editorName(argv[0])
	flags := argv[1:]
	switch {
	case vimFamily[name]:
		if guiVim[name] || hasFlag(flags, "-g") {
			return hasFlag(flags, "-f", "--nofork")
		}
		return true
	case name == "emacs":
		// emacs opens a window unless held in the terminal.
		return hasFlag(flags, "-nw", "-t", "--tty", "-tty")
	case name == "emacsclient":
		// The opposite of emacs: emacsclient waits for C-x # unless told not
		// to. -c and -t choose the frame, not whether it waits.
		return !hasFlag(flags, "-n", "--no-wait")
	case name == "kate" || name == "kwrite":
		return hasFlag(flags, "-b", "--block")
	case terminalEditors[name]:
		return true
	default:
		// An unrecognised command carrying a wait flag is taken at its word;
		// otherwise assume it is a launcher. Guessing wrong that way costs a
		// line of explanation, and guessing wrong the other way costs the
		// user's writing.
		return hasFlag(flags, "-w", "--wait", "--block")
	}
}

// editorName reduces a command to the name the rules are written against:
// case-insensitive, without its directory, and without the suffix Debian's
// alternatives put on vim.tiny and friends.
func editorName(cmd string) string {
	n := strings.ToLower(filepath.Base(cmd))
	if i := strings.IndexByte(n, '.'); i > 0 {
		if base := n[:i]; vimFamily[base] || terminalEditors[base] {
			return base
		}
	}
	return n
}

// hasFlag reports whether any of want appears, accepting the --flag=value form
// for the long ones.
func hasFlag(flags []string, want ...string) bool {
	for _, f := range flags {
		for _, w := range want {
			if f == w {
				return true
			}
			if strings.HasPrefix(w, "--") && strings.HasPrefix(f, w+"=") {
				return true
			}
		}
	}
	return false
}

func firstOnPath(look func(string) (string, error)) string {
	for _, ed := range fallbackOrder {
		if _, err := look(ed); err == nil {
			return ed
		}
	}
	return ""
}

// Runner returns the default Run for an Editor, reporting its choice of editor
// to stderr rather than to os.Stderr directly, so the command owns its output.
func Runner(stderr io.Writer) func(path string) error {
	return func(path string) error {
		argv, note, err := editorFor(os.Getenv, exec.LookPath)
		if err != nil {
			return err
		}
		if note != "" {
			fmt.Fprintln(stderr, "notes: "+note)
		}
		cmd := exec.Command(argv[0], append(argv[1:], path)...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}
}
