package notestore

import (
	"database/sql"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

// Written before search. The shape that matters: a person looking for a note
// remembers a phrase, not a title, and wants the hit shown in context.

func TestSearchMatchesBodyNotJustTitle(t *testing.T) {
	s := openTest(t)
	hits, err := s.Search(SearchOptions{Query: "linked"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("no hit for a word that appears only in the body")
	}
}

// A search that finds nothing is not an error, and must not be reported as one.
func TestSearchWithNoHits(t *testing.T) {
	hits, err := openTest(t).Search(SearchOptions{Query: "nothinglikethisanywhere"})
	if err != nil {
		t.Fatalf("no hits became an error: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("got %d hits", len(hits))
	}
}

// Case-insensitive by default: nobody remembers the capitalisation.
func TestSearchIsCaseInsensitive(t *testing.T) {
	s := openTest(t)
	lower, _ := s.Search(SearchOptions{Query: "alpha"})
	upper, _ := s.Search(SearchOptions{Query: "ALPHA"})
	if len(lower) == 0 || len(lower) != len(upper) {
		t.Errorf("case changed the results: %d vs %d", len(lower), len(upper))
	}
}

// Every hit carries enough context to recognise the note without opening it.
func TestSearchHitsCarryContext(t *testing.T) {
	hits, err := openTest(t).Search(SearchOptions{Query: "linked"})
	if err != nil || len(hits) == 0 {
		t.Fatalf("no hits: %v", err)
	}
	h := hits[0]
	if h.UUID == "" {
		t.Error("hit has no uuid")
	}
	if h.Context == "" {
		t.Error("hit has no context")
	}
	if !strings.Contains(strings.ToLower(h.Context), "linked") {
		t.Errorf("the context does not contain the match: %q", h.Context)
	}
}

// A title match outranks a body match: if the phrase is the note's name, that
// is almost always the note wanted.
func TestSearchRanksTitleMatchesFirst(t *testing.T) {
	hits, err := openTest(t).Search(SearchOptions{Query: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits")
	}
	if !strings.EqualFold(hits[0].Title, "Alpha") {
		t.Errorf("first hit is %q, want the note whose title matches", hits[0].Title)
	}
}

// Trashed notes are excluded by default, like the listing.
func TestSearchExcludesTrashByDefault(t *testing.T) {
	s := openTest(t)
	def, _ := s.Search(SearchOptions{Query: "alpha"})
	all, _ := s.Search(SearchOptions{Query: "alpha", IncludeDeleted: true})
	for _, h := range def {
		if h.Trashed {
			t.Errorf("a trashed note was returned by default: %s", h.Title)
		}
	}
	if len(all) < len(def) {
		t.Error("including deleted returned fewer hits")
	}
}

// A locked note cannot be searched, and must not abort the whole search.
func TestSearchSkipsUnreadableNotes(t *testing.T) {
	if _, err := openTest(t).Search(SearchOptions{Query: "e"}); err != nil {
		t.Errorf("an unreadable note aborted the search: %v", err)
	}
}

// An empty query is a mistake, not a request for everything.
func TestSearchRejectsAnEmptyQuery(t *testing.T) {
	if _, err := openTest(t).Search(SearchOptions{Query: "  "}); err == nil {
		t.Error("an empty query was accepted")
	}
}

// strings.ToLower preserves rune count but not byte length: Ⱥ (U+023A, 2 bytes)
// lowercases to ⱥ (U+2C65, 3 bytes) and İ (U+0130, 2 bytes) to i (1 byte). The
// match offset is found in the lowercased text, so using it on the original
// slices at the wrong place -- too far right it panics and takes down the whole
// search, too far left it silently returns an excerpt that does not contain the
// match. Apple Notes bodies are arbitrary UTF-8, so this is reachable by anyone
// who pastes a phonetic symbol or writes "İstanbul".
func TestSearchSurvivesTextWhoseLowercaseIsADifferentLength(t *testing.T) {
	for _, tc := range []struct{ name, filler string }{
		{"lowercase is wider", "Ⱥ"},    // 2 bytes -> 3: offset runs past the end
		{"lowercase is narrower", "İ"}, // 2 bytes -> 1: offset falls short
		{"kelvin sign", "K"},           // 3 bytes -> 1
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Repeat(tc.filler+" ", 120) + " the target phrase is here"
			s := searchFixture(t, "Padded", body)

			hits, err := s.Search(SearchOptions{Query: "target"})
			if err != nil {
				t.Fatalf("search failed: %v", err)
			}
			if len(hits) == 0 {
				t.Fatal("no hit for a phrase that is in the body")
			}
			// The excerpt exists to show where the match is; one that does not
			// contain it is worse than none, because it reads as if it does.
			if !strings.Contains(strings.ToLower(hits[0].Context), "target") {
				t.Errorf("context does not contain the match: %q", hits[0].Context)
			}
		})
	}
}

// A body in a script that does not separate words with spaces still has to come
// back with something around the match: trimming to word boundaries must not
// consume the entire excerpt.
func TestSearchGivesContextInAScriptWithoutSpaces(t *testing.T) {
	body := strings.Repeat("日本語のテキスト", 20) + "target" + strings.Repeat("続きの文章です", 20)
	s := searchFixture(t, "Japanese", body)

	hits, err := s.Search(SearchOptions{Query: "target"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("no hit")
	}
	// Just the needle and two ellipses is what the boundary walk degenerates to.
	if got := strings.Trim(hits[0].Context, "…"); got == "target" {
		t.Errorf("no surrounding text at all: %q", hits[0].Context)
	}
	if !strings.Contains(hits[0].Context, "target") {
		t.Errorf("context does not contain the match: %q", hits[0].Context)
	}
}

// searchFixture builds a store holding exactly one note with the given title
// and body, for cases where the shared fixture's ASCII content is the reason a
// bug stays invisible.
func searchFixture(t *testing.T, title, body string) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "NoteStore.sqlite")
	dsn := (&url.URL{Scheme: "file", Path: path,
		RawQuery: url.Values{"_pragma": {"journal_mode(WAL)"}}.Encode()}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `CREATE TABLE ZICCLOUDSYNCINGOBJECT (
		Z_PK INTEGER PRIMARY KEY, ZIDENTIFIER TEXT, ZTITLE1 TEXT, ZTITLE2 TEXT,
		ZSNIPPET TEXT, ZFOLDER INTEGER, ZNOTEDATA INTEGER,
		ZCREATIONDATE1 REAL, ZCREATIONDATE REAL, ZCREATIONDATE2 REAL,
		ZMODIFICATIONDATE1 REAL, ZMODIFICATIONDATE REAL,
		ZMARKEDFORDELETION INTEGER, ZISPINNED INTEGER, ZISPASSWORDPROTECTED INTEGER)`)
	mustExec(t, db, `CREATE TABLE ZICNOTEDATA (Z_PK INTEGER PRIMARY KEY, ZNOTE INTEGER, ZDATA BLOB)`)
	mustExec(t, db, `INSERT INTO ZICCLOUDSYNCINGOBJECT (Z_PK, ZIDENTIFIER, ZTITLE2) VALUES (1, 'DefaultFolder-CloudKit', 'Notes')`)
	if _, err := db.Exec(`INSERT INTO ZICCLOUDSYNCINGOBJECT
		(Z_PK, ZIDENTIFIER, ZTITLE1, ZFOLDER, ZNOTEDATA, ZCREATIONDATE1, ZMODIFICATIONDATE1)
		VALUES (100, 'UUID-A', ?, 1, 200, 100000, 500000)`, title); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO ZICNOTEDATA (Z_PK, ZNOTE, ZDATA) VALUES (200, 100, ?)`,
		blob(title+"\n"+body, run(len([]rune(title+"\n"+body)), 0, -2, ""))); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// "Titles rank above bodies" has to hold for every note, not just short ones.
// The body score grew with the number of occurrences and nothing bounded it, so
// a long transcript mentioning a common word enough times outranked the note
// actually named after it -- the exact inversion the ranking exists to prevent.
func TestALongBodyCannotOutrankATitle(t *testing.T) {
	titled := searchFixture(t, "The Plan", "nothing else here")
	titledHits, err := titled.Search(SearchOptions{Query: "plan"})
	if err != nil || len(titledHits) == 0 {
		t.Fatalf("no hit for the titled note: %v", err)
	}

	// A note that says the word a thousand times and is not about it.
	const transcript = "we should plan for that. "
	wordy := searchFixture(t, "Transcript", strings.Repeat(transcript, 1000))
	wordyHits, err := wordy.Search(SearchOptions{Query: "plan"})
	if err != nil || len(wordyHits) == 0 {
		t.Fatalf("no hit for the wordy note: %v", err)
	}

	if wordyHits[0].Score >= titledHits[0].Score {
		t.Errorf("a body with 1000 matches scored %d, at or above the title match's %d",
			wordyHits[0].Score, titledHits[0].Score)
	}
}
