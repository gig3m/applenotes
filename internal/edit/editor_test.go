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
	for _, want := range []string{"omarchy-launch-editor", "does not wait", "NOTES_EDITOR", "nvim"} {
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

// emacs and emacsclient open a window unless held in the terminal, so the bare
// forms are launchers however familiar the names are.
func TestBareEmacsIsTreatedAsALauncher(t *testing.T) {
	for _, ed := range []string{"emacs", "emacsclient"} {
		argv, _, err := editorFor(env(map[string]string{"EDITOR": ed}), have("nano"))
		if err != nil {
			t.Fatal(err)
		}
		if argv[0] != "nano" {
			t.Errorf("%q: argv = %v, want the fallback", ed, argv)
		}
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
func TestBlankEditorIsIgnored(t *testing.T) {
	argv, _, err := editorFor(
		env(map[string]string{"VISUAL": "   ", "EDITOR": ""}), have("nano"))
	if err != nil {
		t.Fatal(err)
	}
	if argv[0] != "nano" {
		t.Errorf("argv = %v, want a blank setting skipped", argv)
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
