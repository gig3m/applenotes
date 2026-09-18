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
	_, err := w.Replace(context.Background(), "UUID-ATTACH", "new body")

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
	if _, err := w.Append(context.Background(), "UUID-ATTACH", "more"); !errors.As(err, &lossy) {
		t.Fatalf("got %v, want ErrLossyRewrite", err)
	}
}

// Formatting that merely flattens must not block a write. It gets past the
// guard and fails later, at the Apple Event.
func TestReplaceAllowsMerelyDegradedNotes(t *testing.T) {
	w := New(openFixture(t))
	// UUID-DEGRADED is an indented, underlined note -- the exact shape that a
	// previous version refused permanently, with no way to override it.
	for _, uuid := range []string{"UUID-PLAIN", "UUID-DEGRADED"} {
		var lossy *ErrLossyRewrite
		if _, err := w.Replace(context.Background(), uuid, "new body"); errors.As(err, &lossy) {
			t.Errorf("%s was refused: %v", uuid, err)
		}
	}
}

// A body that cannot be read is a locked or corrupt note: the case where least
// is known, so the guard must fail closed rather than overwrite it.
func TestReplaceFailsClosedOnUnreadableBody(t *testing.T) {
	w := New(openFixture(t))
	_, err := w.Replace(context.Background(), "UUID-MISSING", "new body")
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
			return w.Replace(context.Background(), u, "new")
		}},
		{"append", func(w *Writer, u string) ([]string, error) {
			return w.Append(context.Background(), u, "more")
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
	_, locked := w.Replace(context.Background(), "UUID-LOCKED", "new")
	// Distinguishable programmatically, not just in prose: a daemon choosing a
	// status code cannot be made to string-match.
	if !errors.Is(locked, notestore.ErrUnreadableBody) {
		t.Errorf("locked note: got %v, want ErrUnreadableBody", locked)
	}
	// And it must say what to do about it: force is the only way through.
	if !strings.Contains(locked.Error(), "force") {
		t.Errorf("locked note: the error does not mention force: %v", locked)
	}
	_, missing := w.Replace(context.Background(), "UUID-NOSUCH", "new")
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
			ZMARKEDFORDELETION INTEGER, ZISPINNED INTEGER, ZISPASSWORDPROTECTED INTEGER)`,
		`CREATE TABLE ZICNOTEDATA (Z_PK INTEGER PRIMARY KEY, ZNOTE INTEGER, ZDATA BLOB)`,
		`CREATE TABLE Z_METADATA (Z_UUID TEXT)`,
		`INSERT INTO Z_METADATA VALUES ('STORE-UUID')`,
		`INSERT INTO ZICCLOUDSYNCINGOBJECT (Z_PK, ZIDENTIFIER, ZTITLE1, ZNOTEDATA) VALUES
			(1, 'UUID-ATTACH', 'Has attachment', 10),
			(2, 'UUID-PLAIN', 'Plain', 11),
			(3, 'UUID-MISSING', 'Unreadable', 12),
			(4, 'UUID-DEGRADED', 'Indented and underlined', 13),
			(5, 'UUID-LOCKED', 'Locked', 14)`,
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

// degradedBlob builds a note with indentation and underlining: formatting a
// rewrite flattens, which must not block the write.
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

	style := append(vfield(1, 100), vfield(4, 1)...) // dot list, indent 1
	run := append(vfield(1, 4), field(2, style)...)
	run = append(run, vfield(6, 1)...) // underlined
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
