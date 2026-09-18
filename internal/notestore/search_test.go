package notestore

import (
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
