package notestore

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// newTestDB builds a database with the columns this package reads. Notes'
// real schema has hundreds of columns; only these matter here.
func newTestDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "NoteStore.sqlite")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mustExec(t, db, `CREATE TABLE ZICCLOUDSYNCINGOBJECT (
		Z_PK INTEGER PRIMARY KEY, ZIDENTIFIER TEXT, ZTITLE1 TEXT, ZTITLE2 TEXT,
		ZSNIPPET TEXT, ZFOLDER INTEGER, ZNOTEDATA INTEGER,
		ZCREATIONDATE1 REAL, ZCREATIONDATE REAL, ZCREATIONDATE2 REAL,
		ZMODIFICATIONDATE1 REAL, ZMODIFICATIONDATE REAL,
		ZMARKEDFORDELETION INTEGER, ZISPINNED INTEGER, ZISPASSWORDPROTECTED INTEGER)`)
	mustExec(t, db, `CREATE TABLE ZICNOTEDATA (Z_PK INTEGER PRIMARY KEY, ZNOTE INTEGER, ZDATA BLOB)`)

	// folders
	mustExec(t, db, `INSERT INTO ZICCLOUDSYNCINGOBJECT (Z_PK, ZIDENTIFIER, ZTITLE2) VALUES
		(1, 'DefaultFolder-CloudKit', 'Notes'),
		(2, 'TrashFolder-CloudKit', 'Recently Deleted'),
		(3, 'FOLDER-UUID-3', 'Southside')`)

	// 100: normal. 101: in Southside. 102: flagged deleted. 103: in the trash
	// folder but not flagged. 104: password-protected.
	mustExec(t, db, `INSERT INTO ZICCLOUDSYNCINGOBJECT
		(Z_PK, ZIDENTIFIER, ZTITLE1, ZSNIPPET, ZFOLDER, ZNOTEDATA, ZCREATIONDATE1, ZMODIFICATIONDATE1, ZMARKEDFORDELETION, ZISPINNED, ZISPASSWORDPROTECTED) VALUES
		(100, 'UUID-A', 'Alpha', 'snip a', 1, 200, 100000, 500000, 0, 1, 0),
		(101, 'UUID-B', 'Bravo', 'snip b', 3, 201, 100000, 400000, 0, 0, 0),
		(102, 'UUID-C', 'Charlie', '',      1, 202, 100000, 300000, 1, 0, 0),
		(103, 'UUID-D', 'Delta',  '',       2, 203, 100000, 200000, 0, 0, 0),
		(104, 'UUID-E', 'Echo',   '',       1, 204, 100000, 100000, 0, 0, 1)`)

	body := blob("Alpha\nlinked", run(6, 0, -2, ""), run(6, 0, -2, "https://x.test/a"))
	stmt := `INSERT INTO ZICNOTEDATA (Z_PK, ZNOTE, ZDATA) VALUES (?, ?, ?)`
	for i, pk := range []int{100, 101, 102, 103, 104} {
		if _, err := db.Exec(stmt, 200+i, pk, body); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func mustExec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.Exec(q); err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
}

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(newTestDB(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestFolders(t *testing.T) {
	got, err := openTest(t).Folders()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d folders, want 3", len(got))
	}
	var trash int
	for _, f := range got {
		if f.Trash() {
			trash++
		}
	}
	if trash != 1 {
		t.Errorf("got %d trash folders, want 1", trash)
	}
}

// Both routes into the trash must be excluded by default: the deletion flag,
// and simply living in the Recently Deleted folder.
func TestNotesExcludesTrashByDefault(t *testing.T) {
	s := openTest(t)
	visible, err := s.Notes(ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, n := range visible {
		titles = append(titles, n.Title)
	}
	if len(visible) != 3 {
		t.Fatalf("got %v, want Alpha, Bravo, Echo", titles)
	}
	// newest first
	if visible[0].Title != "Alpha" || visible[2].Title != "Echo" {
		t.Errorf("wrong order: %v", titles)
	}

	all, err := s.Notes(ListOptions{IncludeDeleted: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 5 {
		t.Errorf("got %d with deleted, want 5", len(all))
	}
}

func TestNotesFolderFilter(t *testing.T) {
	s := openTest(t)
	for _, key := range []string{"Southside", "FOLDER-UUID-3"} {
		got, err := s.Notes(ListOptions{Folder: key})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Title != "Bravo" {
			t.Errorf("%s: got %d notes, want just Bravo", key, len(got))
		}
	}
}

func TestMetaAndBody(t *testing.T) {
	s := openTest(t)
	m, err := s.Meta("UUID-A")
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "Alpha" || m.FolderName != "Notes" || !m.Pinned || m.Locked {
		t.Errorf("unexpected metadata: %+v", m)
	}
	if want := "applenotes:note/UUID-A"; m.DeepLink() != want {
		t.Errorf("got %q want %q", m.DeepLink(), want)
	}
	// 500000s after 2001-01-01 UTC
	if want := time.Unix(500000+coreDataEpoch, 0).UTC(); !m.Modified.Equal(want) {
		t.Errorf("got %v want %v", m.Modified, want)
	}

	body, err := s.Body("UUID-A")
	if err != nil {
		t.Fatal(err)
	}
	if want := "Alpha\n[linked](https://x.test/a)"; body.Markdown() != want {
		t.Errorf("got %q want %q", body.Markdown(), want)
	}
}

func TestLockedNoteIsFlagged(t *testing.T) {
	m, err := openTest(t).Meta("UUID-E")
	if err != nil {
		t.Fatal(err)
	}
	if !m.Locked {
		t.Error("password-protected note not flagged")
	}
}

func TestMissingNote(t *testing.T) {
	s := openTest(t)
	if _, err := s.Meta("nope"); err != ErrNotFound {
		t.Errorf("Meta: got %v want ErrNotFound", err)
	}
	if _, err := s.Body("nope"); err != ErrNotFound {
		t.Errorf("Body: got %v want ErrNotFound", err)
	}
}

func TestOpenMissingFile(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "absent.sqlite")); err == nil {
		t.Error("expected an error for a missing database")
	}
}
