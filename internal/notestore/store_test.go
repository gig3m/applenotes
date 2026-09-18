package notestore

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestDB builds a database with the columns this package reads. Notes'
// real schema has hundreds of columns; only these matter here.
func newTestDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "NoteStore.sqlite")
	// WAL, matching the real database -- read-only access to a WAL database is
	// the thing most likely to break.
	dsn := (&url.URL{Scheme: "file", Path: path,
		RawQuery: url.Values{"_pragma": {"journal_mode(WAL)"}}.Encode()}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("fixture journal_mode = %s, want wal", mode)
	}

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
		(3, 'FOLDER-UUID-3', 'Work')`)
	mustExec(t, db, `INSERT INTO ZICCLOUDSYNCINGOBJECT (Z_PK, ZIDENTIFIER, ZTITLE2, ZMARKEDFORDELETION)
		VALUES (4, 'FOLDER-UUID-4', 'Gone', 1)`)

	// 100: normal. 101: in Work. 102: flagged deleted. 103: in the trash
	// folder but not flagged. 104: password-protected.
	mustExec(t, db, `INSERT INTO ZICCLOUDSYNCINGOBJECT
		(Z_PK, ZIDENTIFIER, ZTITLE1, ZSNIPPET, ZFOLDER, ZNOTEDATA, ZCREATIONDATE1, ZMODIFICATIONDATE1, ZMARKEDFORDELETION, ZISPINNED, ZISPASSWORDPROTECTED) VALUES
		(100, 'UUID-A', 'Alpha', 'snip a', 1, 200, 100000, 500000, 0, 1, 0),
		(101, 'UUID-B', 'Bravo', 'snip b', 3, 201, 100000, 400000, 0, 0, 0),
		(102, 'UUID-C', 'Charlie', '',      1, 202, 100000, 300000, 1, 0, 0),
		(103, 'UUID-D', 'Delta',  '',       2, 203, 100000, 200000, 0, 0, 0),
		(104, 'UUID-E', 'Echo',   '',       1, 204, 100000, 100000, 0, 0, 1)`)

	// 105: no folder. 106: no title. 107: ZNOTEDATA set but no body row.
	// 108: ZMODIFICATIONDATE1 NULL, newest by its fallback column.
	// 109: ZCREATIONDATE1 stored as 0, with a real date in the next column.
	mustExec(t, db, `INSERT INTO ZICCLOUDSYNCINGOBJECT
		(Z_PK, ZIDENTIFIER, ZTITLE1, ZFOLDER, ZNOTEDATA, ZCREATIONDATE1, ZCREATIONDATE, ZMODIFICATIONDATE1, ZMODIFICATIONDATE) VALUES
		(105, 'UUID-F', 'Foxtrot', NULL, 205, 100000, NULL, 50000,  NULL),
		(106, 'UUID-G', NULL,      1,    206, 100000, NULL, 40000,  NULL),
		(107, 'UUID-H', 'Hotel',   1,    303, 100000, NULL, 30000,  NULL),
		(108, 'UUID-I', 'India',   1,    207, 100000, NULL, NULL,   900000),
		(109, 'UUID-J', 'Juliet',  1,    208, 0,      77000, 20000, NULL),
		(110, 'UUID-K', 'Kilo',    4,    209, 100000, NULL,  10000, NULL),
		(111, 'UUID-L', 'Lima',    1,    210, 100000, NULL,  0,     990000)`)

	body := blob("Alpha\nlinked", run(6, 0, -2, ""), run(6, 0, -2, "https://x.test/a"))
	stmt := `INSERT INTO ZICNOTEDATA (Z_PK, ZNOTE, ZDATA) VALUES (?, ?, ?)`
	// Note 107 deliberately gets no row here.
	for i, pk := range []int{100, 101, 102, 103, 104, 105, 106, 108, 109, 110, 111} {
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
	if len(got) != 4 {
		t.Fatalf("got %d folders, want 4", len(got))
	}
	var trash, deleted int
	for _, f := range got {
		if f.Trash() {
			trash++
		}
		if f.Deleted {
			deleted++
		}
	}
	if trash != 1 {
		t.Errorf("got %d trash folders, want 1", trash)
	}
	if deleted != 1 {
		t.Errorf("got %d folders marked deleted, want 1", deleted)
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
	// Alpha, Bravo, Echo, Foxtrot, India, Juliet and the untitled one; Hotel is
	// excluded for having no body row, Charlie and Delta are in the trash.
	// Kilo is excluded too: its folder is marked for deletion.
	if len(visible) != 8 {
		t.Fatalf("got %d notes %v, want 8", len(visible), titles)
	}
	for _, n := range visible {
		if n.Title == "Kilo" {
			t.Error("a note in a deleted folder should not be listed")
		}
	}
	for _, n := range visible {
		if n.Title == "Hotel" {
			t.Error("a note with no body row should not be listed")
		}
	}

	all, err := s.Notes(ListOptions{IncludeDeleted: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 11 {
		t.Errorf("got %d with deleted, want 11", len(all))
	}
}

// A note sorts by the same timestamp the listing displays. Ordering on
// ZMODIFICATIONDATE1 alone puts a note whose date lives in the fallback column
// last while showing it as the newest.
func TestNotesOrderMatchesDisplayedTime(t *testing.T) {
	got, err := openTest(t).Notes(ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Lima's ZMODIFICATIONDATE1 is a stored 0 with the real date in the
	// fallback column, so SQL COALESCE and firstTime must agree that 0 means
	// absent -- otherwise it displays as newest and sorts last.
	if got[0].Title != "Lima" {
		t.Errorf("newest is %q, want Lima", got[0].Title)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Modified.After(got[i-1].Modified) {
			t.Fatalf("not sorted newest-first at %d: %v", i, got[i].Title)
		}
	}
}

// A note with no title still has content and must be reachable.
func TestUntitledNoteIsVisible(t *testing.T) {
	m, err := openTest(t).Meta("UUID-G")
	if err != nil {
		t.Fatalf("untitled note unreachable: %v", err)
	}
	if m.Title != "" {
		t.Errorf("got title %q, want empty", m.Title)
	}
}

// A NULL ZFOLDER must not drop the note: that is what the LEFT JOIN is for.
func TestNoteWithoutFolder(t *testing.T) {
	m, err := openTest(t).Meta("UUID-F")
	if err != nil {
		t.Fatalf("folderless note unreachable: %v", err)
	}
	if m.FolderName != "" {
		t.Errorf("got folder %q, want empty", m.FolderName)
	}
}

// list and show must agree: a note with no body row is listed by neither.
func TestNoteWithoutBodyRowIsHidden(t *testing.T) {
	s := openTest(t)
	if _, err := s.Meta("UUID-H"); err != ErrNotFound {
		t.Errorf("Meta: got %v want ErrNotFound", err)
	}
	if _, err := s.Body("UUID-H"); err != ErrNotFound {
		t.Errorf("Body: got %v want ErrNotFound", err)
	}
}

// A stored 0 must not end the fallback chain and mask a real timestamp.
func TestZeroTimestampFallsThrough(t *testing.T) {
	m, err := openTest(t).Meta("UUID-J")
	if err != nil {
		t.Fatal(err)
	}
	if m.Created.IsZero() {
		t.Fatal("creation date was masked by a stored 0")
	}
	if got, want := m.Created.Unix(), int64(77000+coreDataEpoch); got != want {
		t.Errorf("got %d want %d", got, want)
	}
}

// The trash flag alone misses a note that is in the trash by its folder.
func TestTrashedCoversBothRoutes(t *testing.T) {
	all, err := openTest(t).Notes(ListOptions{IncludeDeleted: true})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, n := range all {
		if n.Trashed {
			seen[n.Title] = true
		}
	}
	for _, want := range []string{"Charlie", "Delta", "Kilo"} {
		if !seen[want] {
			t.Errorf("%s not reported as trashed", want)
		}
	}
}

// The database must be opened read-only, and a path containing URI
// metacharacters must not defeat that.
func TestOpenIsReadOnly(t *testing.T) {
	s := openTest(t)
	if _, err := s.db.Exec("CREATE TABLE zzz_written (a)"); err == nil {
		t.Fatal("the database was opened writable")
	}
}

func TestOpenPathWithURIMetacharacters(t *testing.T) {
	src := newTestDB(t)
	for _, name := range []string{"note #1.sqlite", "note?mode=rw.sqlite", "100%25.sqlite"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			dst := filepath.Join(dir, name)
			copyFile(t, src, dst)

			s, err := Open(dst)
			if err != nil {
				t.Fatalf("could not open %q: %v", name, err)
			}
			defer s.Close()
			if _, err := s.Folders(); err != nil {
				t.Fatalf("Folders: %v", err)
			}
			if _, err := s.db.Exec("CREATE TABLE zzz_written (a)"); err == nil {
				t.Error("mode=ro was discarded: the database is writable")
			}
			// Nothing may be created alongside it.
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				if n := e.Name(); n != name && !strings.HasPrefix(n, name) {
					t.Errorf("Open created a stray file: %q", n)
				}
			}
		})
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNotesFolderFilter(t *testing.T) {
	s := openTest(t)
	for _, key := range []string{"Work", "FOLDER-UUID-3"} {
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
	// Core Data counts seconds from 2001-01-01 UTC. Written out rather than
	// derived from coreDataEpoch, so an off-by-one in the constant fails here.
	if want := time.Date(2001, 1, 6, 18, 53, 20, 0, time.UTC); !m.Modified.Equal(want) {
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

// sharedDB builds a fixture with the CloudKit sharing columns, which the main
// fixture deliberately lacks so that both schema shapes stay covered.
func sharedDB(t *testing.T) string {
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
		`INSERT INTO ZICCLOUDSYNCINGOBJECT (Z_PK, ZIDENTIFIER, ZTITLE2) VALUES (1, 'DefaultFolder-CloudKit', 'Notes')`,
		`INSERT INTO ZICCLOUDSYNCINGOBJECT (Z_PK, ZIDENTIFIER, ZTITLE1, ZFOLDER, ZNOTEDATA, ZZONEOWNERNAME, ZSERVERSHAREDATA) VALUES
			(10, 'UUID-MINE',      'Mine',           1, 200, NULL,           NULL),
			(11, 'UUID-THEIRS',    'Theirs',         1, 201, '_otheruser',   X'00'),
			(12, 'UUID-SHARED-OUT','Mine, shared',   1, 202, NULL,           X'00'),
			(13, 'UUID-NO-RECORD', 'Foreign, no share record', 1, 203, '_otheruser', NULL)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	body := blob("text", run(4, 0, StyleBody, ""))
	for i, pk := range []int{10, 11, 12, 13} {
		if _, err := db.Exec(`INSERT INTO ZICNOTEDATA VALUES (?, ?, ?)`, 200+i, pk, body); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	return path
}

// Ownership separates three states, and conflating any two of them is a real
// mistake: "shared" alone would make notes this account owns read-only, and
// owner alone would miss a note shared out.
func TestOwnershipIsReadFromTheDatabase(t *testing.T) {
	s, err := Open(sharedDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for _, tc := range []struct {
		uuid                 string
		shared, withMe, byMe bool
		owner                string
	}{
		{"UUID-MINE", false, false, false, ""},
		{"UUID-THEIRS", true, true, false, "_otheruser"},
		{"UUID-SHARED-OUT", true, false, true, ""},
		// A foreign zone with no share record yet: still someone else's note.
		// Treating it as unshared would let a write through unannounced.
		{"UUID-NO-RECORD", true, true, false, "_otheruser"},
	} {
		m, err := s.Meta(tc.uuid)
		if err != nil {
			t.Fatalf("%s: %v", tc.uuid, err)
		}
		if m.Shared != tc.shared || m.SharedWithMe() != tc.withMe ||
			m.SharedByMe() != tc.byMe || m.Owner != tc.owner {
			t.Errorf("%s: shared=%v withMe=%v byMe=%v owner=%q, want %v/%v/%v/%q",
				tc.uuid, m.Shared, m.SharedWithMe(), m.SharedByMe(), m.Owner,
				tc.shared, tc.withMe, tc.byMe, tc.owner)
		}
	}
}

// Notes predating CloudKit sharing have no such columns, and selecting a column
// that is not there fails the whole query -- so every note would become
// unreadable to gain a field that is only sometimes present.
func TestAnOlderSchemaWithoutSharingColumnsStillWorks(t *testing.T) {
	s := openTest(t) // the main fixture has no sharing columns
	notes, err := s.Notes(ListOptions{})
	if err != nil {
		t.Fatalf("listing failed on a schema without sharing columns: %v", err)
	}
	if len(notes) == 0 {
		t.Fatal("no notes")
	}
	for _, n := range notes {
		if n.Shared || n.SharedWithMe() || n.Owner != "" {
			t.Errorf("%s reported sharing from a schema that cannot express it", n.UUID)
		}
	}
	if _, err := s.Meta(notes[0].UUID); err != nil {
		t.Errorf("Meta failed on a schema without sharing columns: %v", err)
	}
}
