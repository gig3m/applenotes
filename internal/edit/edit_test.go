package edit

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// Written before the editor. Editing a note means fetching it, handing it to
// $EDITOR, and writing back what comes out -- and every property here is about
// not writing back when we should not.

type fakeStore struct {
	body     string
	fetchErr error
	written  []string
	writeErr error
}

func (f *fakeStore) Fetch(ctx context.Context, uuid string) (string, error) {
	return f.body, f.fetchErr
}

func (f *fakeStore) Write(ctx context.Context, uuid, markdown string, force bool) ([]string, error) {
	f.written = append(f.written, markdown)
	return nil, f.writeErr
}

// Property: an unchanged buffer is not written back. Rewriting a note is lossy
// for anything Markdown cannot express, so doing it for nothing is a real cost.
func TestUnchangedBufferIsNotWritten(t *testing.T) {
	s := &fakeStore{body: "hello\nworld"}
	// The editor waits, then changes nothing -- a real no-op edit, as opposed
	// to an editor that never waited at all.
	ed := &Editor{Store: s, Run: func(path string) error { return nil }}
	wrote, err := ed.Edit(context.Background(), "UUID")
	if err != nil {
		t.Fatal(err)
	}
	if wrote {
		t.Error("reported a write for an unchanged buffer")
	}
	if len(s.written) != 0 {
		t.Errorf("wrote %q for an unchanged buffer", s.written)
	}
}

// Property: if the editor fails, nothing is written. A crashed editor must not
// be read as "the user emptied the note".
func TestEditorFailureWritesNothing(t *testing.T) {
	s := &fakeStore{body: "hello"}
	ed := &Editor{Store: s, Run: func(path string) error {
		os.WriteFile(path, []byte(""), 0o600) // even if it mangled the file
		return errors.New("editor died")
	}}
	if _, err := ed.Edit(context.Background(), "UUID"); err == nil {
		t.Fatal("expected an error")
	}
	if len(s.written) != 0 {
		t.Errorf("wrote %q after the editor failed", s.written)
	}
}

// Property: a failed fetch never leads to a write. Editing a note we could not
// read would replace it with whatever the editor started from.
func TestFetchFailureWritesNothing(t *testing.T) {
	s := &fakeStore{fetchErr: errors.New("unreachable")}
	ed := &Editor{Store: s, Run: func(path string) error {
		os.WriteFile(path, []byte("whatever"), 0o600)
		return nil
	}}
	if _, err := ed.Edit(context.Background(), "UUID"); err == nil {
		t.Fatal("expected an error")
	}
	if len(s.written) != 0 {
		t.Errorf("wrote %q after a failed fetch", s.written)
	}
}

// Property: emptying the buffer does not empty the note. That is almost always
// a mistake, and it is unrecoverable.
func TestEmptiedBufferIsRefused(t *testing.T) {
	s := &fakeStore{body: "hello"}
	ed := &Editor{Store: s, Run: func(path string) error {
		return os.WriteFile(path, []byte("   \n"), 0o600)
	}}
	if _, err := ed.Edit(context.Background(), "UUID"); err == nil {
		t.Fatal("expected a refusal")
	}
	if len(s.written) != 0 {
		t.Errorf("emptied the note: %q", s.written)
	}
}

// Property: a real edit is written back exactly as the editor left it.
func TestEditIsWrittenVerbatim(t *testing.T) {
	s := &fakeStore{body: "hello"}
	const edited = "hello\n\nand a second paragraph with  two spaces "
	ed := &Editor{Store: s, Run: func(path string) error {
		return os.WriteFile(path, []byte(edited), 0o600)
	}}
	wrote, err := ed.Edit(context.Background(), "UUID")
	if err != nil {
		t.Fatal(err)
	}
	if !wrote {
		t.Error("did not report the write")
	}
	if len(s.written) != 1 || s.written[0] != edited {
		t.Errorf("got %q, want %q", s.written, edited)
	}
}

// Property: the buffer is written somewhere only this user can read, and is
// removed afterwards. A note can hold anything.
func TestBufferIsPrivateAndRemoved(t *testing.T) {
	s := &fakeStore{body: "secret"}
	var seen string
	ed := &Editor{Store: s, Run: func(path string) error {
		seen = path
		fi, err := os.Stat(path)
		if err != nil {
			return err
		}
		if perm := fi.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("buffer is readable by others: %o", perm)
		}
		return os.WriteFile(path, []byte("changed"), 0o600)
	}}
	if _, err := ed.Edit(context.Background(), "UUID"); err != nil {
		t.Fatal(err)
	}
	if seen == "" {
		t.Fatal("the editor was never invoked")
	}
	if _, err := os.Stat(seen); !os.IsNotExist(err) {
		t.Errorf("buffer %s was left behind", seen)
	}
}

// Property: a refused rewrite is reported with what would be lost, and the
// buffer is kept so the work is not thrown away.
func TestRefusedWriteKeepsTheBuffer(t *testing.T) {
	s := &fakeStore{body: "hello", writeErr: errors.New("contains attachments")}
	var path string
	ed := &Editor{Store: s, Run: func(p string) error {
		path = p
		return os.WriteFile(p, []byte("edited"), 0o600)
	}}
	_, err := ed.Edit(context.Background(), "UUID")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("the error does not say where the work is: %v", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("the buffer was discarded after a failed write: %v", statErr)
	}
	os.Remove(path)
}

// A GUI editor launched detached returns before the user has typed anything.
// Reporting "no changes" is true and useless: the editor is still open and the
// edit is about to be lost.

// A genuine no-op edit, where the editor did wait, is still silent.
