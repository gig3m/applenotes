package notesapp

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gig3m/applenotes/internal/notestore"
	_ "modernc.org/sqlite"
)

// The guards below fire before any Apple Event is sent, so these run anywhere.
// osascript does not exist on the machine running them, which is what makes a
// guard failure obvious: the test would report an exec error instead.

func TestReplaceRefusesToDestroyContent(t *testing.T) {
	w := New(openFixture(t))
	_, err := w.Replace(context.Background(), "UUID-ATTACH", "new body", false)

	var lossy *ErrLossyRewrite
	if !errors.As(err, &lossy) {
		t.Fatalf("got %v, want ErrLossyRewrite", err)
	}
	if !strings.Contains(err.Error(), "attachments") {
		t.Errorf("error does not name what would be lost: %v", err)
	}
}

func TestAppendRefusesToDestroyContent(t *testing.T) {
	w := New(openFixture(t))
	var lossy *ErrLossyRewrite
	if _, err := w.Append(context.Background(), "UUID-ATTACH", "more", false); !errors.As(err, &lossy) {
		t.Fatalf("got %v, want ErrLossyRewrite", err)
	}
}

// Formatting that merely flattens must not block a write. It gets past the
// guard and fails later, at the Apple Event.
func TestReplaceAllowsMerelyDegradedNotes(t *testing.T) {
	w := New(openFixture(t))
	// UUID-DEGRADED carries formatting a rewrite flattens -- the exact shape
	// that a previous version refused permanently, with no way to override it.
	for _, uuid := range []string{"UUID-PLAIN", "UUID-DEGRADED"} {
		var lossy *ErrLossyRewrite
		if _, err := w.Replace(context.Background(), uuid, "new body", false); errors.As(err, &lossy) {
			t.Errorf("%s was refused: %v", uuid, err)
		}
	}
}

// A body that cannot be read is a locked or corrupt note: the case where least
// is known, so the guard must fail closed rather than overwrite it.
func TestReplaceFailsClosedOnUnreadableBody(t *testing.T) {
	w := New(openFixture(t))
	_, err := w.Replace(context.Background(), "UUID-MISSING", "new body", false)
	if err == nil {
		t.Fatal("an unreadable note was overwritten")
	}
	if !strings.Contains(err.Error(), "cannot check") {
		t.Errorf("got %v, want a refusal to proceed", err)
	}
}

// Formatting that will flatten is reported on both writing paths. It is a
// return value, not a field on the Writer, so that concurrent callers cannot
// see each other's.
func TestDegradedFormattingIsReturned(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*Writer, string) ([]string, error)
	}{
		{"replace", func(w *Writer, u string) ([]string, error) {
			return w.Replace(context.Background(), u, "new", false)
		}},
		{"append", func(w *Writer, u string) ([]string, error) {
			return w.Append(context.Background(), u, "more", false)
		}},
	} {
		w := New(openFixture(t))
		// The Apple Event fails here -- osascript does not exist -- but the
		// features are computed before it and returned alongside the error.
		got, _ := tc.call(w, "UUID-DEGRADED")
		if len(got) == 0 {
			t.Errorf("%s: nothing reported for a note that will flatten", tc.name)
		}
		if plain, _ := tc.call(w, "UUID-PLAIN"); len(plain) != 0 {
			t.Errorf("%s: reported %v for a plain note", tc.name, plain)
		}
	}
}

// A locked note and a typo both fail, but they are different situations.
func TestLockedNoteIsDistinguishedFromMissing(t *testing.T) {
	w := New(openFixture(t))
	_, locked := w.Replace(context.Background(), "UUID-LOCKED", "new", false)
	// Distinguishable programmatically, not just in prose: a daemon choosing a
	// status code cannot be made to string-match.
	if !errors.Is(locked, notestore.ErrUnreadableBody) {
		t.Errorf("locked note: got %v, want ErrUnreadableBody", locked)
	}
	// And it must say what to do about it: force is the only way through.
	if !strings.Contains(locked.Error(), "force") {
		t.Errorf("locked note: the error does not mention force: %v", locked)
	}
	_, missing := w.Replace(context.Background(), "UUID-NOSUCH", "new", false)
	if !errors.Is(missing, notestore.ErrNotFound) || errors.Is(missing, notestore.ErrUnreadableBody) {
		t.Errorf("missing note: got %v, want a plain not-found", missing)
	}
}

func openFixture(t *testing.T) *notestore.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "NoteStore.sqlite")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE ZICCLOUDSYNCINGOBJECT (Z_PK INTEGER PRIMARY KEY, ZIDENTIFIER TEXT,
			ZTITLE1 TEXT, ZTITLE2 TEXT, ZSNIPPET TEXT, ZFOLDER INTEGER, ZNOTEDATA INTEGER,
			ZCREATIONDATE1 REAL, ZCREATIONDATE REAL, ZCREATIONDATE2 REAL,
			ZMODIFICATIONDATE1 REAL, ZMODIFICATIONDATE REAL,
			ZMARKEDFORDELETION INTEGER, ZISPINNED INTEGER, ZISPASSWORDPROTECTED INTEGER,
			ZZONEOWNERNAME TEXT, ZSERVERSHAREDATA BLOB)`,
		`CREATE TABLE ZICNOTEDATA (Z_PK INTEGER PRIMARY KEY, ZNOTE INTEGER, ZDATA BLOB)`,
		`CREATE TABLE Z_METADATA (Z_UUID TEXT)`,
		`INSERT INTO Z_METADATA VALUES ('STORE-UUID')`,
		`INSERT INTO ZICCLOUDSYNCINGOBJECT (Z_PK, ZIDENTIFIER, ZTITLE1, ZNOTEDATA) VALUES
			(1, 'UUID-ATTACH', 'Has attachment', 10),
			(2, 'UUID-PLAIN', 'Plain', 11),
			(3, 'UUID-MISSING', 'Unreadable', 12),
			(4, 'UUID-DEGRADED', 'An indented subheading', 13),
			(5, 'UUID-LOCKED', 'Locked', 14),
			(7, 'UUID-THEIRS', 'Owned by someone else', 15),
			(8, 'UUID-SHARED-BY-ME', 'Mine, shared out', 16)`,
		// ZZONEOWNERNAME set means the note lives in another account's CloudKit
		// zone: shared with this user. Share data without a zone owner is a
		// note this account owns and has shared out, which stays writable.
		`UPDATE ZICCLOUDSYNCINGOBJECT SET ZZONEOWNERNAME = '_someoneelse' WHERE Z_PK = 7`,
		`UPDATE ZICCLOUDSYNCINGOBJECT SET ZSERVERSHAREDATA = X'00' WHERE Z_PK IN (7, 8)`,
		`UPDATE ZICCLOUDSYNCINGOBJECT SET ZISPASSWORDPROTECTED = 1 WHERE Z_PK = 5`,
		`INSERT INTO ZICCLOUDSYNCINGOBJECT (Z_PK, ZIDENTIFIER, ZTITLE2) VALUES (6, 'FOLDER-UUID', 'A folder')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	// Row 3 gets a ZICNOTEDATA row whose blob is not valid gzip, standing in
	// for a locked or corrupt note: addressable, but its body cannot be read.
	for _, r := range []struct {
		pk, note int
		blob     []byte
	}{
		{10, 1, noteBlob(t, true)},
		{11, 2, noteBlob(t, false)},
		{12, 3, []byte("not gzip")},
		{13, 4, degradedBlob(t)},
		{14, 5, nil}, // locked: the row exists, the body does not
		{15, 7, noteBlob(t, false)},
		{16, 8, noteBlob(t, false)},
	} {
		if _, err := db.Exec(`INSERT INTO ZICNOTEDATA VALUES (?, ?, ?)`, r.pk, r.note, r.blob); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	s, err := notestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// degradedBlob builds a note whose formatting a rewrite really does flatten,
// which must not block the write.
func degradedBlob(t *testing.T) []byte {
	t.Helper()
	varint := func(v uint64) []byte {
		var b []byte
		for v >= 0x80 {
			b = append(b, byte(v)|0x80)
			v >>= 7
		}
		return append(b, byte(v))
	}
	field := func(num int, data []byte) []byte {
		out := append(varint(uint64(num)<<3|2), varint(uint64(len(data)))...)
		return append(out, data...)
	}
	vfield := func(num int, v uint64) []byte {
		return append(varint(uint64(num)<<3), varint(v)...)
	}

	// A subheading, indented. Deliberately not a list and not underlining:
	// both of those survive a rewrite now, so a fixture built from them would
	// assert that nothing degrades while claiming to test that something does.
	style := append(vfield(1, 2), vfield(4, 1)...) // subheading, indent 1
	run := append(vfield(1, 4), field(2, style)...)
	note := append(field(2, []byte("text")), field(5, run)...)
	raw := field(2, append(vfield(2, 1), field(3, note)...))

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	return buf.Bytes()
}

// noteBlob builds a ZICNOTEDATA blob: a gzipped protobuf of one text run,
// optionally carrying an attachment so the note reports as destructive.
func noteBlob(t *testing.T, withAttachment bool) []byte {
	t.Helper()
	varint := func(v uint64) []byte {
		var b []byte
		for v >= 0x80 {
			b = append(b, byte(v)|0x80)
			v >>= 7
		}
		return append(b, byte(v))
	}
	field := func(num int, wire uint64, data []byte) []byte {
		out := append(varint(uint64(num)<<3|wire), varint(uint64(len(data)))...)
		return append(out, data...)
	}
	vfield := func(num int, v uint64) []byte {
		return append(varint(uint64(num)<<3), varint(v)...)
	}

	run := vfield(1, 4) // AttributeRun.length
	if withAttachment {
		info := append(field(1, 2, []byte("ATT-ID")), field(2, 2, []byte("public.jpeg"))...)
		run = append(run, field(12, 2, info)...)
	}
	note := append(field(2, 2, []byte("text")), field(5, 2, run)...)
	doc := append(vfield(2, 1), field(3, 2, note)...)
	raw := field(2, 2, doc)

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	return buf.Bytes()
}

// A note shared with this user lives in the owner's CloudKit zone, so editing
// it is not a local act: the change syncs to them and to everyone else on the
// share. Default to refusing, and make the opt-in say so by name.
//
// This is deliberately below force. Accepting that a rewrite will flatten your
// own formatting says nothing about whether you meant to edit someone else's
// note, so -force must not open this gate.
func TestWritesToSomeoneElsesNoteAreRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*Writer, string, bool) error
	}{
		{"replace", func(w *Writer, u string, allow bool) error {
			_, err := w.Replace(context.Background(), u, "new", allow)
			return err
		}},
		{"replace -force", func(w *Writer, u string, allow bool) error {
			return w.ReplaceForce(context.Background(), u, "new", allow)
		}},
		{"append", func(w *Writer, u string, allow bool) error {
			_, err := w.Append(context.Background(), u, "more", allow)
			return err
		}},
		{"delete", func(w *Writer, u string, allow bool) error {
			return w.Delete(context.Background(), u, allow)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := New(openFixture(t))
			var shared *ErrSharedNote
			if err := tc.call(w, "UUID-THEIRS", false); !errors.As(err, &shared) {
				t.Errorf("a note owned by someone else was not refused: %v", err)
			}
			// Opting in gets past the gate. The Apple Event fails after it --
			// osascript does not exist here -- which is how we know the gate
			// was the only thing in the way.
			var stillShared *ErrSharedNote
			if err := tc.call(w, "UUID-THEIRS", true); errors.As(err, &stillShared) {
				t.Errorf("the opt-in did not get past the gate: %v", err)
			}
		})
	}
}

// A note this account owns and has shared with others is still this account's
// note. Refusing it would make every shared note read-only, which is not what
// sharing means.
func TestANoteSharedOutStaysWritable(t *testing.T) {
	w := New(openFixture(t))
	var shared *ErrSharedNote
	if _, err := w.Replace(context.Background(), "UUID-SHARED-BY-ME", "new", false); errors.As(err, &shared) {
		t.Errorf("a note this account owns was refused as someone else's: %v", err)
	}
	if _, err := w.Replace(context.Background(), "UUID-PLAIN", "new", false); errors.As(err, &shared) {
		t.Errorf("an ordinary note was refused as someone else's: %v", err)
	}
}

// Fails closed. If ownership cannot be established the write does not happen:
// the cost of guessing wrong is damage to someone else's note.
func TestUnknownOwnershipRefusesTheWrite(t *testing.T) {
	w := New(openFixture(t))
	err := w.ReplaceForce(context.Background(), "UUID-NO-SUCH-NOTE", "new", false)
	if err == nil {
		t.Fatal("a note whose ownership could not be checked was written")
	}
	// The error code alone proves nothing here: the write would fail later
	// anyway because there is no such note. It has to fail at the ownership
	// check, or a database error would let a foreign note through.
	if !strings.Contains(err.Error(), "yours to write") {
		t.Errorf("failed for the wrong reason: %v", err)
	}
	if !errors.Is(err, notestore.ErrNotFound) {
		t.Errorf("the underlying cause was not kept: %v", err)
	}
}
