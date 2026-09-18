package notesd

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/gig3m/applenotes/internal/notestore"
	_ "modernc.org/sqlite"
)

// newTestServer builds a server over a synthetic notes database. Writes reach
// osascript, which does not exist on the machine running these tests -- that is
// deliberate: it means a guard failing open shows up as an exec error rather
// than passing silently.
func newTestServer(t *testing.T) *testServer {
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
		`INSERT INTO ZICCLOUDSYNCINGOBJECT (Z_PK, ZIDENTIFIER, ZTITLE2) VALUES
			(1, 'DefaultFolder-CloudKit', 'Notes'),
			(2, 'TrashFolder-CloudKit', 'Recently Deleted')`,
		`INSERT INTO ZICCLOUDSYNCINGOBJECT (Z_PK, ZIDENTIFIER, ZTITLE1, ZFOLDER, ZNOTEDATA, ZMODIFICATIONDATE1) VALUES
			(10, 'UUID-PLAIN', 'Plain', 1, 100, 500000),
			(11, 'UUID-ATTACH', 'Has attachment', 1, 101, 400000),
			(12, 'UUID-DEGRADED', 'Indented and underlined', 1, 102, 300000)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	for _, r := range []struct {
		pk, note int
		blob     []byte
	}{
		{100, 10, noteBlob(t, plainRun())},
		{101, 11, noteBlob(t, attachmentRun())},
		{102, 12, noteBlob(t, degradedRun())},
	} {
		if _, err := db.Exec(`INSERT INTO ZICNOTEDATA VALUES (?, ?, ?)`, r.pk, r.note, r.blob); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	store, err := notestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return &testServer{h: New(store, testToken).Handler(), dbPath: path}
}

// --- protobuf fixtures -------------------------------------------------------

func varint(v uint64) []byte {
	var b []byte
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func field(num int, data []byte) []byte {
	return append(append(varint(uint64(num)<<3|2), varint(uint64(len(data)))...), data...)
}

func vfield(num int, v uint64) []byte {
	return append(varint(uint64(num)<<3), varint(v)...)
}

func plainRun() []byte { return vfield(1, 4) }

func attachmentRun() []byte {
	info := append(field(1, []byte("ATT-ID")), field(2, []byte("public.jpeg"))...)
	return append(vfield(1, 4), field(12, info)...)
}

func degradedRun() []byte {
	style := append(vfield(1, 100), vfield(4, 1)...) // dot list, indented
	run := append(vfield(1, 4), field(2, style)...)
	return append(run, vfield(6, 1)...) // underlined
}

func noteBlob(t *testing.T, run []byte) []byte {
	t.Helper()
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
