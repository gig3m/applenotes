package notestore

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// coreDataEpoch is 2001-01-01 UTC; Notes stores timestamps as seconds from it.
const coreDataEpoch = 978307200

// DefaultPath is where Notes keeps its database for the current user.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Group Containers",
		"group.com.apple.notes", "NoteStore.sqlite"), nil
}

// Folder is a Notes folder. Apple's built-in folders carry fixed identifiers
// rather than UUIDs: DefaultFolder-CloudKit, TrashFolder-CloudKit and
// SystemPaper-CloudKit (Quick Notes).
type Folder struct {
	ID      int64
	UUID    string
	Name    string
	Deleted bool
}

// Trash reports whether this is the Recently Deleted folder.
func (f Folder) Trash() bool { return f.UUID == "TrashFolder-CloudKit" }

// NoteMeta is a note's index entry. The body is fetched separately because
// decoding it costs a gunzip per note.
type NoteMeta struct {
	ID         int64
	UUID       string // ZIDENTIFIER: stable across devices, unlike the Core Data row id
	Title      string
	Snippet    string
	FolderID   int64
	FolderName string
	Created    time.Time
	Modified   time.Time
	FolderUUID string
	Deleted    bool // ZMARKEDFORDELETION on the note itself
	Trashed    bool // in Recently Deleted by any route: the flag, the folder, or a deleted folder
	Pinned     bool
	Locked     bool // password-protected; the body is not readable
}

// Store is a read-only view of a Notes database.
type Store struct {
	db *sql.DB
}

// Open opens the database read-only. Notes runs in WAL mode, so this reads
// whatever the app has committed; it never writes.
func Open(path string) (*Store, error) {
	if path == "" {
		p, err := DefaultPath()
		if err != nil {
			return nil, err
		}
		path = p
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("notestore: %w", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// The DSN must be built as a URL, not concatenated. SQLite parses file:
	// DSNs as URIs, so a '#' in the path truncates the query string and
	// silently discards mode=ro -- which then opens read-write and creates the
	// file. A '?' lets an earlier mode= win. Both are reachable from an
	// ordinary path like "~/Desktop/backup #1/NoteStore.sqlite".
	dsn := (&url.URL{
		Scheme: "file",
		Path:   abs,
		RawQuery: url.Values{
			"mode": {"ro"},
			// WAL readers can still hit an exclusive lock during wal-index
			// recovery or a checkpoint; without a timeout the query fails
			// immediately instead of retrying. Matters for a polling daemon.
			"_pragma": {"busy_timeout(5000)"},
		}.Encode(),
	}).String()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection, kept for the life of the Store: nothing here benefits from
	// concurrency, and a long-lived handle avoids re-mapping the -shm on every
	// poll. Note this makes the handle unusable for anything that holds a Tx or
	// open Rows while issuing a second query -- that would deadlock in the pool.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("notestore: opening %s: %w (if this is a copied database, its -wal and -shm files must be copied too, and the directory must be writable)", abs, err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Folders lists every folder, including Recently Deleted.
func (s *Store) Folders() ([]Folder, error) {
	rows, err := s.db.Query(`
		SELECT Z_PK, COALESCE(ZIDENTIFIER, ''), ZTITLE2, COALESCE(ZMARKEDFORDELETION, 0)
		FROM ZICCLOUDSYNCINGOBJECT
		WHERE ZTITLE2 IS NOT NULL
		ORDER BY ZTITLE2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Folder
	for rows.Next() {
		var f Folder
		var del int
		if err := rows.Scan(&f.ID, &f.UUID, &f.Name, &del); err != nil {
			return nil, err
		}
		f.Deleted = del != 0
		out = append(out, f)
	}
	return out, rows.Err()
}

// ListOptions filters a note listing.
type ListOptions struct {
	IncludeDeleted bool   // include notes in Recently Deleted
	Folder         string // folder name or UUID; empty means all
}

// A row is a note when a body row points back at it. Testing ZNOTEDATA IS NOT
// NULL only checks the foreign key, so a note whose body has not arrived yet --
// the shape CloudKit produces mid-sync -- would list but fail to open. Title is
// not required either: a note whose first line is empty can have none.
const noteSelect = `
	SELECT n.Z_PK,
	       COALESCE(n.ZIDENTIFIER, ''),
	       COALESCE(n.ZTITLE1, ''),
	       COALESCE(n.ZSNIPPET, ''),
	       COALESCE(n.ZFOLDER, 0),
	       COALESCE(f.ZTITLE2, ''),
	       COALESCE(f.ZIDENTIFIER, ''),
	       n.ZCREATIONDATE1, n.ZCREATIONDATE, n.ZCREATIONDATE2,
	       n.ZMODIFICATIONDATE1, n.ZMODIFICATIONDATE,
	       COALESCE(n.ZMARKEDFORDELETION, 0),
	       COALESCE(n.ZISPINNED, 0),
	       COALESCE(n.ZISPASSWORDPROTECTED, 0),
	       COALESCE(f.ZMARKEDFORDELETION, 0)
	FROM ZICCLOUDSYNCINGOBJECT n
	LEFT JOIN ZICCLOUDSYNCINGOBJECT f ON f.Z_PK = n.ZFOLDER
	WHERE EXISTS (SELECT 1 FROM ZICNOTEDATA d WHERE d.ZNOTE = n.Z_PK AND d.ZDATA IS NOT NULL)`

// Notes lists note metadata, newest first.
func (s *Store) Notes(opt ListOptions) ([]NoteMeta, error) {
	q := noteSelect
	var args []any
	if !opt.IncludeDeleted {
		// A note is in the trash either by its own flag or by living in the
		// Recently Deleted folder.
		q += ` AND COALESCE(n.ZMARKEDFORDELETION, 0) = 0
		       AND COALESCE(f.ZIDENTIFIER, '') <> 'TrashFolder-CloudKit'
		       AND COALESCE(f.ZMARKEDFORDELETION, 0) = 0`
	}
	if opt.Folder != "" {
		q += ` AND (f.ZTITLE2 = ? OR f.ZIDENTIFIER = ?)`
		args = append(args, opt.Folder, opt.Folder)
	}
	// NULLIF, not bare COALESCE: SQL COALESCE stops at the first non-NULL
	// including a stored 0, while firstTime treats 0 as absent. Without this
	// the two disagree and a note displays as newest while sorting last.
	q += ` ORDER BY COALESCE(NULLIF(n.ZMODIFICATIONDATE1, 0), NULLIF(n.ZMODIFICATIONDATE, 0), 0) DESC, n.Z_PK DESC`

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNotes(rows)
}

// ErrNotFound is returned when no note matches.
var ErrNotFound = errors.New("notestore: note not found")

// Meta looks a note up by its ZIDENTIFIER UUID.
func (s *Store) Meta(uuid string) (NoteMeta, error) {
	rows, err := s.db.Query(noteSelect+` AND n.ZIDENTIFIER = ? ORDER BY n.Z_PK LIMIT 1`, uuid)
	if err != nil {
		return NoteMeta{}, err
	}
	defer rows.Close()
	list, err := scanNotes(rows)
	if err != nil {
		return NoteMeta{}, err
	}
	if len(list) == 0 {
		return NoteMeta{}, ErrNotFound
	}
	return list[0], nil
}

// Body fetches and decodes a note's contents by UUID.
func (s *Store) Body(uuid string) (*Note, error) {
	var blob []byte
	// Ordered to match Meta, so a duplicated UUID cannot make show print one
	// row's body under another row's metadata.
	err := s.db.QueryRow(`
		SELECT d.ZDATA
		FROM ZICCLOUDSYNCINGOBJECT n
		JOIN ZICNOTEDATA d ON d.ZNOTE = n.Z_PK
		WHERE n.ZIDENTIFIER = ? AND d.ZDATA IS NOT NULL
		ORDER BY n.Z_PK LIMIT 1`, uuid).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return Decode(blob)
}

func scanNotes(rows *sql.Rows) ([]NoteMeta, error) {
	var out []NoteMeta
	for rows.Next() {
		var n NoteMeta
		var c1, c2, c3, m1, m2 sql.NullFloat64
		var del, pin, locked, folderDel int
		if err := rows.Scan(&n.ID, &n.UUID, &n.Title, &n.Snippet, &n.FolderID,
			&n.FolderName, &n.FolderUUID, &c1, &c2, &c3, &m1, &m2,
			&del, &pin, &locked, &folderDel); err != nil {
			return nil, err
		}
		// The fallback is resolved here rather than with COALESCE, which stops
		// at the first non-NULL: a stored 0.0 would end the chain and mask a
		// real timestamp in the next column.
		n.Created = firstTime(c1, c2, c3)
		n.Modified = firstTime(m1, m2)
		n.Pinned, n.Locked = pin != 0, locked != 0
		n.Deleted = del != 0
		n.Trashed = n.Deleted || n.FolderUUID == "TrashFolder-CloudKit" || folderDel != 0
		out = append(out, n)
	}
	return out, rows.Err()
}

// firstTime returns the first timestamp that is present and non-zero.
func firstTime(vals ...sql.NullFloat64) time.Time {
	for _, v := range vals {
		if v.Valid && v.Float64 != 0 {
			return coreDataTime(v.Float64)
		}
	}
	return time.Time{}
}

func coreDataTime(v float64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	sec, frac := int64(v), v-float64(int64(v))
	return time.Unix(sec+coreDataEpoch, int64(frac*1e9)).UTC()
}

// DeepLink returns the applenotes: URL for a note. It is built from the
// ZIDENTIFIER UUID; the Core Data row id that AppleScript reports is local to
// one machine and will not resolve on another device.
func (n NoteMeta) DeepLink() string { return "applenotes:note/" + n.UUID }

// StoreUUID is the Core Data persistent store identifier, the middle component
// of the x-coredata:// ids AppleScript uses.
func (s *Store) StoreUUID() (string, error) {
	var u string
	err := s.db.QueryRow(`SELECT Z_UUID FROM Z_METADATA`).Scan(&u)
	if err != nil {
		return "", fmt.Errorf("notestore: reading store uuid: %w", err)
	}
	return u, nil
}

// ScriptID converts a note's ZIDENTIFIER UUID into the x-coredata:// id that
// Notes.app's AppleScript interface addresses notes by.
//
// The two identifier spaces are different and only one is portable: the UUID is
// the CloudKit record name and is the same on every device, while the
// x-coredata id embeds a row number local to this machine's database. Anything
// stored or exchanged uses the UUID; the x-coredata form is derived here at the
// moment it is handed to AppleScript.
func (s *Store) ScriptID(uuid string) (string, error) {
	store, err := s.StoreUUID()
	if err != nil {
		return "", err
	}
	// The same predicate Meta and Body use. ZICCLOUDSYNCINGOBJECT holds
	// folders and accounts too, so without it a folder UUID would be formatted
	// as a note id and handed to AppleScript.
	var pk int64
	err = s.db.QueryRow(`
		SELECT Z_PK FROM ZICCLOUDSYNCINGOBJECT n
		WHERE n.ZIDENTIFIER = ?
		  AND EXISTS (SELECT 1 FROM ZICNOTEDATA d WHERE d.ZNOTE = n.Z_PK AND d.ZDATA IS NOT NULL)
		ORDER BY Z_PK LIMIT 1`, uuid).Scan(&pk)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("x-coredata://%s/ICNote/p%d", store, pk), nil
}

// UUIDForPK returns the portable UUID for a Core Data row id.
func (s *Store) UUIDForPK(pk int64) (string, error) {
	var u string
	err := s.db.QueryRow(
		`SELECT ZIDENTIFIER FROM ZICCLOUDSYNCINGOBJECT WHERE Z_PK = ?`, pk).Scan(&u)
	if errors.Is(err, sql.ErrNoRows) || u == "" {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("notestore: looking up uuid for row %d: %w", pk, err)
	}
	return u, nil
}
