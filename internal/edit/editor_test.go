package edit

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// env builds a lookup over a fixed set of variables.
func env(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

// have reports the named commands as present on PATH and nothing else.
func have(names ...string) func(string) (string, error) {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	return func(n string) (string, error) {
		if set[n] {
			return "/usr/bin/" + n, nil
		}
		return "", exec.ErrNotFound
	}
}

// The bug this package exists to avoid: a launcher that returns before the user
// has typed anything, so the buffer reads back unchanged and the edit is lost.
// It must never be chosen while something that waits is available.
func TestLauncherEditorIsNotUsed(t *testing.T) {
	argv, note, err := editorFor(
		env(map[string]string{"EDITOR": "omarchy-launch-editor --inline"}),
		have("nvim", "nano"))
	if err != nil {
		t.Fatal(err)
	}
	if argv[0] == "omarchy-launch-editor" {
		t.Fatal("used an editor that does not wait")
	}
	if argv[0] != "nvim" {
		t.Errorf("argv = %v, want the first terminal editor on PATH", argv)
	}
	for _, want := range []string{"omarchy-launch-editor", "cannot confirm", "NOTES_EDITOR", "nvim"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note does not mention %q: %q", want, note)
		}
	}
	// Advice that does not work is worse than none: their launcher has no wait
	// flag, so the message must not invent one for it.
	if strings.Contains(note, "omarchy-launch-editor -w") ||
		strings.Contains(note, "omarchy-launch-editor --wait") {
		t.Errorf("suggested a flag the command may not have: %q", note)
	}
}

// The fallback is a repair, not a preference: anything that waits is used as
// configured, and silently.
func TestEditorsThatWaitAreUsedAsConfigured(t *testing.T) {
	for _, ed := range []string{
		"nvim", "vim", "vi", "nano", "helix", "hx", "kak", "micro",
		"code -w", "code --wait", "zed --wait", "subl -w", "emacs -nw",
		"/opt/weird/path/to/vim", "emacsclient -t",
	} {
		argv, note, err := editorFor(env(map[string]string{"EDITOR": ed}), have("nano"))
		if err != nil {
			t.Errorf("%q: %v", ed, err)
			continue
		}
		if strings.Join(argv, " ") != ed {
			t.Errorf("%q: argv = %v, want it used as written", ed, argv)
		}
		if note != "" {
			t.Errorf("%q: explained a choice that needs no explaining: %q", ed, note)
		}
	}
}

// emacs and emacsclient are opposites, and treating them alike gets one of them
// wrong whichever way you guess. emacs opens a window unless held in the
// terminal; emacsclient waits for C-x # unless told not to, and -c/-t choose
// the frame rather than the waiting.
func TestEmacsAndEmacsclientAreOpposites(t *testing.T) {
	for _, tc := range []struct {
		ed    string
		waits bool
	}{
		{"emacs", false}, // opens a window and returns
		{"emacs -nw", true},
		{"emacs -t", true},
		{"emacsclient", true}, // waits
		{"emacsclient -c", true},
		{"emacsclient -t", true},
		{"emacsclient -n", false},    // --no-wait is the whole point
		{"emacsclient -t -n", false}, // a documented pairing
		{"emacsclient --no-wait", false},
	} {
		argv, _, err := editorFor(env(map[string]string{"EDITOR": tc.ed}), have("nano"))
		if err != nil {
			t.Fatal(err)
		}
		used := strings.Join(argv, " ")
		if tc.waits && used != tc.ed {
			t.Errorf("%q: used %q, want it as configured", tc.ed, used)
		}
		if !tc.waits && used != "nano" {
			t.Errorf("%q: used %q, want the fallback", tc.ed, used)
		}
	}
}

// The vim family is the one place -w is not a wait flag: there it means "record
// keystrokes to this file" and takes an argument. gvim, and vim -g, are GUI vim
// and fork unless given -f. Reading a vim script log as a promise to wait is
// the data-loss direction, reached through the allow-list meant to prevent it.
func TestGUIVimIsNotMistakenForTerminalVim(t *testing.T) {
	for _, tc := range []struct {
		ed    string
		waits bool
	}{
		{"vim", true},
		{"vim -w /tmp/keys.log", true}, // terminal vim waits regardless
		{"vim -g", false},              // GUI vim by another name
		{"vim -g -f", true},
		{"gvim", false},
		{"gvim -w /tmp/keys.log", false}, // -w here is a keystroke log
		{"gvim -f", true},
		{"gvim --nofork", true},
		{"mvim", false},
		{"view", true},
		{"vimdiff", true},
	} {
		argv, _, err := editorFor(env(map[string]string{"EDITOR": tc.ed}), have("nano"))
		if err != nil {
			t.Fatal(err)
		}
		used := strings.Join(argv, " ")
		if tc.waits && used != tc.ed {
			t.Errorf("%q: used %q, want it as configured", tc.ed, used)
		}
		if !tc.waits && used != "nano" {
			t.Errorf("%q: used %q, want the fallback", tc.ed, used)
		}
	}
}

// VISUAL naming a GUI editor and EDITOR naming a terminal one is the convention
// these two variables exist for. Stopping at the first one set turned that
// convention into a failure: a launcher in VISUAL hid a perfectly good EDITOR,
// and with nothing on PATH it produced a hard error naming an editor that was
// sitting right there.
func TestALauncherInVisualDoesNotHideAUsableEditor(t *testing.T) {
	argv, note, err := editorFor(
		env(map[string]string{"VISUAL": "gedit", "EDITOR": "vim"}), have("nano"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(argv, " ") != "vim" {
		t.Errorf("argv = %v, want $EDITOR", argv)
	}
	if note != "" {
		t.Errorf("explained a choice that needed no explaining: %q", note)
	}

	// The same, with no fallback available: it must not claim there was nothing
	// to use.
	argv, _, err = editorFor(env(map[string]string{"VISUAL": "gedit", "EDITOR": "vim"}), have())
	if err != nil {
		t.Fatalf("no editor found although $EDITOR named one: %v", err)
	}
	if strings.Join(argv, " ") != "vim" {
		t.Errorf("argv = %v, want $EDITOR", argv)
	}
}

// Debian's alternatives install vim as vim.tiny or vim.basic, and a name can
// arrive capitalised. Falling back from a real terminal editor is harmless but
// looks like the tool ignored the machine.
func TestEditorNamesAreMatchedLoosely(t *testing.T) {
	for _, ed := range []string{"vim.tiny", "vim.basic", "VIM", "/usr/bin/vim.nox", "ED"} {
		argv, _, err := editorFor(env(map[string]string{"EDITOR": ed}), have("nano"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(argv, " ") != ed {
			t.Errorf("%q: used %v, want it as configured", ed, argv)
		}
	}
}

// kate's wait flag is -b, which in the vim family means "binary" -- so it is
// accepted for kate and nowhere else.
func TestKateBlockFlag(t *testing.T) {
	for _, tc := range []struct {
		ed    string
		waits bool
	}{
		{"kate -b", true},
		{"kate --block", true},
		{"kate", false},
		{"vim -b", true},       // terminal vim, waits for its own reason
		{"some-gui -b", false}, // -b is not a general wait flag
	} {
		argv, _, err := editorFor(env(map[string]string{"EDITOR": tc.ed}), have("nano"))
		if err != nil {
			t.Fatal(err)
		}
		used := strings.Join(argv, " ")
		if tc.waits && used != tc.ed {
			t.Errorf("%q: used %q, want it as configured", tc.ed, used)
		}
		if !tc.waits && used != "nano" {
			t.Errorf("%q: used %q, want the fallback", tc.ed, used)
		}
	}
}

// The --flag=value spelling is as common as the separated one.
func TestWaitFlagInTheEqualsForm(t *testing.T) {
	for _, ed := range []string{"code --wait", "code --wait=true", "zed --wait"} {
		argv, _, err := editorFor(env(map[string]string{"EDITOR": ed}), have("nano"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(argv, " ") != ed {
			t.Errorf("%q: used %v, want it as configured", ed, argv)
		}
	}
}

// An editor under a directory with a space in its name is a real path on macOS,
// and splitting it on spaces makes it unnameable.
func TestAnEditorPathContainingSpaces(t *testing.T) {
	const p = "/Applications/My Editor/bin/edit"
	look := func(n string) (string, error) {
		if n == p || n == "nano" {
			return n, nil
		}
		return "", exec.ErrNotFound
	}
	argv, _, err := editorFor(env(map[string]string{"NOTES_EDITOR": p}), look)
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) != 1 || argv[0] != p {
		t.Errorf("argv = %v, want the path kept whole", argv)
	}
}

// NOTES_EDITOR is the escape hatch, so it is obeyed exactly -- including a
// choice this package would otherwise second-guess.
func TestNotesEditorIsObeyedVerbatim(t *testing.T) {
	argv, note, err := editorFor(
		env(map[string]string{"NOTES_EDITOR": "my-launcher", "EDITOR": "nvim"}),
		have("nvim"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(argv, " ") != "my-launcher" {
		t.Errorf("argv = %v, want NOTES_EDITOR obeyed over $EDITOR", argv)
	}
	if note != "" {
		t.Errorf("argued with an explicit choice: %q", note)
	}
}

func TestVisualWinsOverEditor(t *testing.T) {
	argv, _, err := editorFor(
		env(map[string]string{"VISUAL": "vim", "EDITOR": "nano"}), have("nano"))
	if err != nil {
		t.Fatal(err)
	}
	if argv[0] != "vim" {
		t.Errorf("argv = %v, want VISUAL", argv)
	}
}

// Nothing configured is the common case on a fresh box and must still work.
func TestUnsetEditorFallsBackToPath(t *testing.T) {
	argv, note, err := editorFor(env(nil), have("nano", "vi"))
	if err != nil {
		t.Fatal(err)
	}
	if argv[0] != "nano" {
		t.Errorf("argv = %v, want the first terminal editor on PATH", argv)
	}
	if note != "" {
		t.Errorf("explained an unremarkable default: %q", note)
	}
}

// Preference order matters: vi exists nearly everywhere, so choosing it while a
// better editor is installed would look like the fallback ignored the machine.
func TestPathFallbackPrefersTheBetterEditor(t *testing.T) {
	argv, _, err := editorFor(env(nil), have("vi", "nano", "nvim"))
	if err != nil {
		t.Fatal(err)
	}
	if argv[0] != "nvim" {
		t.Errorf("argv = %v, want nvim ahead of nano and vi", argv)
	}
}

// With nothing to fall back to, say so rather than run the launcher and lose
// the edit.
func TestNoUsableEditorIsAnError(t *testing.T) {
	_, _, err := editorFor(env(map[string]string{"EDITOR": "my-launcher"}), have())
	if err == nil {
		t.Fatal("no error when there was nothing that could work")
	}
	for _, want := range []string{"my-launcher", "NOTES_EDITOR"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
	if _, _, err := editorFor(env(nil), have()); err == nil {
		t.Fatal("no error with nothing configured and nothing installed")
	}
}

// An empty or whitespace-only setting is unset, not a command named "".
// A blank setting is unset, not a command named "". It has to be checked
// through NOTES_EDITOR: that is the branch that returns the setting verbatim,
// so an untrimmed blank would come back as an empty argv and panic at exec.
// Through VISUAL the blank is caught further down by accident, and the test
// would pass with the trimming removed.
func TestBlankEditorIsIgnored(t *testing.T) {
	for _, env0 := range []map[string]string{
		{"NOTES_EDITOR": "   ", "EDITOR": "vim"},
		{"NOTES_EDITOR": "\t\n", "EDITOR": "vim"},
		{"VISUAL": "   ", "EDITOR": ""},
	} {
		argv, _, err := editorFor(env(env0), have("nano"))
		if err != nil {
			t.Fatalf("%v: %v", env0, err)
		}
		if len(argv) == 0 {
			t.Fatalf("%v: empty argv, which panics at exec", env0)
		}
		if argv[0] == "" {
			t.Errorf("%v: argv = %v, want a blank setting skipped", env0, argv)
		}
	}
}

func TestLookPathErrorsOtherThanNotFoundDoNotPanic(t *testing.T) {
	_, _, err := editorFor(env(nil), func(string) (string, error) {
		return "", errors.New("permission denied")
	})
	if err == nil {
		t.Fatal("want an error when no editor could be resolved")
	}
}

// editorFor's argv goes straight to exec.Command, which indexes argv[0]. A
// blank or whitespace-only setting must never reach it as an empty slice, from
// any variable, by any route.
func TestEditorForNeverReturnsAnEmptyCommand(t *testing.T) {
	blanks := []string{"", " ", "\t", "\n", "  \t \n ", "\u00a0"}
	for _, key := range []string{"NOTES_EDITOR", "VISUAL", "EDITOR"} {
		for _, b := range blanks {
			for _, look := range []func(string) (string, error){have("nano"), have()} {
				argv, _, err := editorFor(env(map[string]string{key: b}), look)
				if err != nil {
					continue // no editor anywhere is a legitimate answer
				}
				if len(argv) == 0 || argv[0] == "" {
					t.Fatalf("%s=%q gave argv %v, which panics at exec", key, b, argv)
				}
			}
		}
	}
}
