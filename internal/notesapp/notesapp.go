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

	// Runner is how scripts are executed. Tests replace it to assert what
	// would have reached osascript.
	Runner applescript.Runner
}

func New(s *notestore.Store) *Writer {
	return &Writer{store: s, Runner: applescript.Osascript}
}

func (w *Writer) run(ctx context.Context, src string, args ...string) (string, error) {
	r := w.Runner
	if r == nil {
		r = applescript.Osascript
	}
	return r.Run(ctx, src, args...)
}

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
	scriptID, err := w.run(ctx, createScript, folder, notestore.ToHTML(markdown))
	if err != nil {
		return "", err
	}
	return w.uuidFor(scriptID)
}

// ErrSharedNote reports a write aimed at a note owned by another account.
//
// A write here is not local. A note shared with this user lives in the owner's
// CloudKit zone, so editing it syncs the change to them and to everyone else on
// the share. That is a different decision from accepting formatting loss, so it
// is a separate gate: force does not open it, and nothing bypasses it except
// asking for it by name.
type ErrSharedNote struct{ Owner string }

func (e *ErrSharedNote) Error() string {
	return "this note belongs to another iCloud account and is read-only here; " +
		"editing it would sync the change to its owner"
}

// checkOwnership refuses a write to a note this account does not own.
//
// Fails closed: if ownership cannot be determined the write does not happen,
// because the cost of being wrong is someone else's note. The exception is a
// database old enough to lack the sharing columns, where Owner is empty for
// every note -- there is nothing to know there, and refusing every write would
// be worse than the risk.
func (w *Writer) checkOwnership(uuid string, allowShared bool) error {
	if allowShared {
		return nil
	}
	meta, err := w.store.Meta(uuid)
	if err != nil {
		return fmt.Errorf("cannot tell whether this note is yours to write: %w", err)
	}
	if meta.SharedWithMe() {
		return &ErrSharedNote{Owner: meta.Owner}
	}
	return nil
}

// ErrLossyRewrite reports that a note holds content a Markdown rewrite would
// destroy outright -- attachments or checklists, not merely formatting that
// would flatten.
type ErrLossyRewrite struct{ Features []string }

func (e *ErrLossyRewrite) Error() string {
	return "this note contains " + strings.Join(e.Features, ", ") +
		", which a Markdown rewrite would destroy"
}

// Append adds Markdown to the end of a note.
//
// There is no non-destructive way to do this. Notes offers no "append" verb, so
// the note must be rewritten whole, and both routes to its existing body lose
// something: reading through AppleScript drops every hyperlink, and rewriting
// from Markdown drops anything Markdown cannot express.
//
// So the body is read from SQLite, which preserves links, and the note is
// checked first for content the rewrite would destroy -- attachments and
// checklists. If it has any, this refuses with *ErrLossyRewrite rather than
// damaging the note. Formatting that would merely flatten is reported by
// Note.Degrades and does not block the write.
//
// Two further caveats apply even when it succeeds. The read comes from the
// database, so an edit still buffered in Notes.app is not included -- and is
// overwritten. And Markdown metacharacters in the existing prose are re-escaped
// on the way through.
// Append returns the formatting the rewrite flattened. It is a return value
// rather than a callback on the Writer so that a server handling concurrent
// requests cannot report one request's loss in another's response.
func (w *Writer) Append(ctx context.Context, uuid, markdown string, allowShared bool) (degraded []string, err error) {
	body, err := w.store.Body(uuid)
	if err != nil {
		return nil, w.explain(uuid, err)
	}
	if lost := body.Destroys(); len(lost) > 0 {
		return nil, &ErrLossyRewrite{Features: lost}
	}
	existing := body.Markdown()
	if existing != "" {
		existing += "\n"
	}
	return body.Degrades(), w.ReplaceForce(ctx, uuid, existing+markdown, allowShared)
}

const replaceScript = `on run argv
	set noteID to item 1 of argv
	set newHTML to item 2 of argv
	tell application "Notes"
		set body of note id noteID to newHTML
	end tell
end run`

// Replace overwrites a note's entire body with Markdown.
//
// It refuses with *ErrLossyRewrite when the note holds content the new body
// cannot carry, because the overwhelmingly common use is show-edit-replace and
// that would silently discard attachments and checklists. Formatting that
// merely flattens is not grounds for refusing; Note.Degrades reports that
// separately. ReplaceForce skips the check for a caller that means it.
func (w *Writer) Replace(ctx context.Context, uuid, markdown string, allowShared bool) (degraded []string, err error) {
	// Fails closed. A body that cannot be read is a locked or corrupt note --
	// exactly the case where least is known and most could be lost -- so an
	// unreadable note is refused rather than silently overwritten.
	body, err := w.store.Body(uuid)
	if err != nil {
		return nil, w.explain(uuid, err)
	}
	if lost := body.Destroys(); len(lost) > 0 {
		return nil, &ErrLossyRewrite{Features: lost}
	}
	return body.Degrades(), w.ReplaceForce(ctx, uuid, markdown, allowShared)
}

// explain turns a failed body read into an error a caller can act on. A missing
// note and an unreadable one are both refusals, but they are different
// situations and only one of them has -force as a way through.
func (w *Writer) explain(uuid string, err error) error {
	if !errors.Is(err, notestore.ErrNotFound) && !errors.Is(err, notestore.ErrUnreadableBody) {
		return fmt.Errorf("cannot check what this rewrite would destroy: %w", err)
	}
	ok, existsErr := w.store.Exists(uuid)
	if existsErr != nil {
		return existsErr
	}
	if !ok {
		return err // genuinely no such note
	}
	return fmt.Errorf("%w: what a rewrite would destroy cannot be checked; use force to overwrite it anyway (%v)",
		notestore.ErrUnreadableBody, err)
}

// ReplaceForce overwrites a note's body without checking what that discards.
// It still refuses a note owned by another account: force is about accepting
// loss in your own note, not about writing in someone else's.
func (w *Writer) ReplaceForce(ctx context.Context, uuid, markdown string, allowShared bool) error {
	if err := w.checkOwnership(uuid, allowShared); err != nil {
		return err
	}
	id, err := w.store.ScriptID(uuid)
	if err != nil {
		return err
	}
	_, err = w.run(ctx, replaceScript, id, notestore.ToHTML(markdown))
	return err
}

const deleteScript = `on run argv
	tell application "Notes" to delete note id (item 1 of argv)
end run`

// Delete moves a note to Recently Deleted. Notes keeps it there for 30 days, so
// this is recoverable.
func (w *Writer) Delete(ctx context.Context, uuid string, allowShared bool) error {
	if err := w.checkOwnership(uuid, allowShared); err != nil {
		return err
	}
	id, err := w.store.ScriptID(uuid)
	if err != nil {
		return err
	}
	_, err = w.run(ctx, deleteScript, id)
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
