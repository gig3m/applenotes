package notestore

import (
	"database/sql"
	"errors"
	"fmt"
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
	Deleted    bool
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
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("notestore: opening %s: %w", path, err)
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

const noteSelect = `
	SELECT n.Z_PK,
	       COALESCE(n.ZIDENTIFIER, ''),
	       COALESCE(n.ZTITLE1, ''),
	       COALESCE(n.ZSNIPPET, ''),
	       COALESCE(n.ZFOLDER, 0),
	       COALESCE(f.ZTITLE2, ''),
	       COALESCE(n.ZCREATIONDATE1, n.ZCREATIONDATE, n.ZCREATIONDATE2, 0),
	       COALESCE(n.ZMODIFICATIONDATE1, n.ZMODIFICATIONDATE, 0),
	       COALESCE(n.ZMARKEDFORDELETION, 0),
	       COALESCE(n.ZISPINNED, 0),
	       COALESCE(n.ZISPASSWORDPROTECTED, 0)
	FROM ZICCLOUDSYNCINGOBJECT n
	LEFT JOIN ZICCLOUDSYNCINGOBJECT f ON f.Z_PK = n.ZFOLDER
	WHERE n.ZTITLE1 IS NOT NULL AND n.ZNOTEDATA IS NOT NULL`

// Notes lists note metadata, newest first.
func (s *Store) Notes(opt ListOptions) ([]NoteMeta, error) {
	q := noteSelect
	var args []any
	if !opt.IncludeDeleted {
		// A note is in the trash either by its own flag or by living in the
		// Recently Deleted folder.
		q += ` AND COALESCE(n.ZMARKEDFORDELETION, 0) = 0
		       AND COALESCE(f.ZIDENTIFIER, '') <> 'TrashFolder-CloudKit'`
	}
	if opt.Folder != "" {
		q += ` AND (f.ZTITLE2 = ? OR f.ZIDENTIFIER = ?)`
		args = append(args, opt.Folder, opt.Folder)
	}
	q += ` ORDER BY n.ZMODIFICATIONDATE1 DESC`

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
	rows, err := s.db.Query(noteSelect+` AND n.ZIDENTIFIER = ?`, uuid)
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
	err := s.db.QueryRow(`
		SELECT d.ZDATA
		FROM ZICCLOUDSYNCINGOBJECT n
		JOIN ZICNOTEDATA d ON d.ZNOTE = n.Z_PK
		WHERE n.ZIDENTIFIER = ? AND d.ZDATA IS NOT NULL`, uuid).Scan(&blob)
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
		var created, modified float64
		var del, pin, locked int
		if err := rows.Scan(&n.ID, &n.UUID, &n.Title, &n.Snippet, &n.FolderID,
			&n.FolderName, &created, &modified, &del, &pin, &locked); err != nil {
			return nil, err
		}
		n.Created = coreDataTime(created)
		n.Modified = coreDataTime(modified)
		n.Deleted, n.Pinned, n.Locked = del != 0, pin != 0, locked != 0
		out = append(out, n)
	}
	return out, rows.Err()
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
