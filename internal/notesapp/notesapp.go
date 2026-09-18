// Package notesapp writes to Notes.app through Apple Events.
//
// Reads never come through here. AppleScript silently drops hyperlink hrefs
// when reading a note body, so reading is the SQLite package's job and this
// package only writes.
//
// # Writes are not immediately visible to readers
//
// Notes.app holds changes in memory and persists them to NoteStore.sqlite on
// its own schedule. Measured on macOS 15: a delete was still absent from the
// database 60 seconds later, and only landed when Notes.app quit. In between,
// the two disagree in both directions -- Notes.app no longer had the note while
// the database still showed it live.
//
// So a write here is not observable through notestore.Store for an unbounded
// period, and read-after-write will return stale data. Anything that needs to
// confirm a write must either poll for the change or track it separately; it
// cannot assume the next read reflects it.
package notesapp

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gig3m/applenotes/internal/applescript"
	"github.com/gig3m/applenotes/internal/notestore"
)

// Writer creates and modifies notes. It needs a read-only Store to translate
// portable note UUIDs into the machine-local ids AppleScript addresses.
type Writer struct {
	store *notestore.Store
}

func New(s *notestore.Store) *Writer { return &Writer{store: s} }

const createScript = `on run argv
	set folderName to item 1 of argv
	set noteHTML to item 2 of argv
	tell application "Notes"
		tell default account
			if folderName is "" then
				set theNote to make new note with properties {body:noteHTML}
			else
				set theNote to make new note at folder folderName with properties {body:noteHTML}
			end if
			return id of theNote
		end tell
	end tell
end run`

// ErrNotYetVisible reports that a note was written but has not reached
// NoteStore.sqlite yet, so its portable UUID cannot be determined. Notes.app
// persists on its own schedule; the note exists regardless.
var ErrNotYetVisible = errors.New("notesapp: note created but not yet in the database")

// Create makes a note from Markdown and returns its portable UUID.
//
// The title is not set separately: Notes derives it from the body's first line,
// and setting both yields the title twice.
//
// It returns ErrNotYetVisible when the note has not been persisted yet, which
// is the common case immediately after a write. The note has still been
// created; only its UUID is unknown.
func (w *Writer) Create(ctx context.Context, folder, markdown string) (string, error) {
	scriptID, err := applescript.Run(ctx, createScript, folder, notestore.ToHTML(markdown))
	if err != nil {
		return "", err
	}
	return w.uuidFor(scriptID)
}

// Append adds Markdown to the end of a note.
//
// The existing body is read from SQLite and the whole note rewritten, rather
// than concatenated through AppleScript. Reading a body through AppleScript
// drops every hyperlink in it, so the obvious implementation silently destroys
// links in the note being appended to.
//
// Because the read comes from the database, this appends to the note as last
// persisted. Notes.app buffers writes, so an edit made on the Mac in the last
// few moments may not be included.
func (w *Writer) Append(ctx context.Context, uuid, markdown string) error {
	body, err := w.store.Body(uuid)
	if err != nil {
		return err
	}
	existing := body.Markdown()
	if existing != "" {
		existing += "\n"
	}
	return w.Replace(ctx, uuid, existing+markdown)
}

const replaceScript = `on run argv
	set noteID to item 1 of argv
	set newHTML to item 2 of argv
	tell application "Notes"
		set body of note id noteID to newHTML
	end tell
end run`

// Replace overwrites a note's entire body with Markdown.
func (w *Writer) Replace(ctx context.Context, uuid, markdown string) error {
	id, err := w.store.ScriptID(uuid)
	if err != nil {
		return err
	}
	_, err = applescript.Run(ctx, replaceScript, id, notestore.ToHTML(markdown))
	return err
}

const deleteScript = `on run argv
	tell application "Notes" to delete note id (item 1 of argv)
end run`

// Delete moves a note to Recently Deleted. Notes keeps it there for 30 days, so
// this is recoverable.
func (w *Writer) Delete(ctx context.Context, uuid string) error {
	id, err := w.store.ScriptID(uuid)
	if err != nil {
		return err
	}
	_, err = applescript.Run(ctx, deleteScript, id)
	return err
}

// uuidFor maps a freshly created note's x-coredata id back to its portable
// UUID.
//
// It deliberately does not fall back to returning the x-coredata id: that id is
// local to this machine and no command here accepts it, so handing it back
// would produce a "note not found" later, nondeterministically depending on
// when Notes flushed.
func (w *Writer) uuidFor(scriptID string) (string, error) {
	i := strings.LastIndex(scriptID, "/p")
	if i < 0 {
		return "", fmt.Errorf("notesapp: unrecognised note id %q", scriptID)
	}
	pk, err := strconv.ParseInt(scriptID[i+2:], 10, 64)
	if err != nil {
		return "", fmt.Errorf("notesapp: unrecognised note id %q", scriptID)
	}
	uuid, err := w.store.UUIDForPK(pk)
	if errors.Is(err, notestore.ErrNotFound) {
		return "", ErrNotYetVisible
	}
	if err != nil {
		return "", err
	}
	return uuid, nil
}
