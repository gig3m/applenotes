package notestore

import (
	"errors"
	"sort"
	"strings"
	"unicode"
)

// Search over note bodies, not just titles. Apple Notes' primary verb is
// finding something you half-remember a phrase from, and a list of titles
// cannot answer that.
//
// It decodes every note to search it. That is fine at the scale a personal
// library reaches -- a few hundred notes is a few megabytes of gzip -- and it
// avoids maintaining an index that could disagree with the database Notes.app
// rewrites underneath us.

type SearchOptions struct {
	Query          string
	IncludeDeleted bool
	Folder         string
	// Limit caps the hits returned. Zero means a sensible default.
	Limit int
}

// Hit is one matching note, with enough context to recognise it without
// opening it.
type Hit struct {
	NoteMeta
	// Context is the matching text with a little either side.
	Context string
	// Score orders the results; higher is better.
	Score int
}

const (
	defaultSearchLimit = 50
	contextRadius      = 60
)

var errEmptyQuery = errors.New("notestore: search needs something to look for")

// Search returns notes whose title or body contains the query, best first.
func (s *Store) Search(opt SearchOptions) ([]Hit, error) {
	query := strings.TrimSpace(opt.Query)
	if query == "" {
		return nil, errEmptyQuery
	}
	needle := strings.ToLower(query)
	limit := opt.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}

	notes, err := s.Notes(ListOptions{IncludeDeleted: opt.IncludeDeleted, Folder: opt.Folder})
	if err != nil {
		return nil, err
	}

	var hits []Hit
	for _, n := range notes {
		// A note whose body cannot be read -- locked, or not yet synced -- is
		// skipped rather than failing the search: one unreadable note must not
		// cost the user every other result.
		body, err := s.Body(n.UUID)
		if err != nil {
			if strings.Contains(strings.ToLower(n.Title), needle) {
				hits = append(hits, Hit{NoteMeta: n, Context: n.Title, Score: titleScore(n.Title, needle)})
			}
			continue
		}
		text := body.PlainText()
		idx := strings.Index(strings.ToLower(text), needle)
		titleIdx := strings.Index(strings.ToLower(n.Title), needle)
		if idx < 0 && titleIdx < 0 {
			continue
		}

		h := Hit{NoteMeta: n}
		switch {
		case titleIdx >= 0:
			// A phrase that is the note's name is almost always the note
			// wanted, so it outranks a body match.
			h.Score = titleScore(n.Title, needle)
			h.Context = excerpt(text, idx, needle)
			if idx < 0 {
				h.Context = n.Title
			}
		default:
			h.Score = 100 + strings.Count(strings.ToLower(text), needle)
			h.Context = excerpt(text, idx, needle)
		}
		hits = append(hits, h)
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Modified.After(hits[j].Modified)
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// titleScore ranks title matches above every body match, and an exact title
// above a partial one.
func titleScore(title, needle string) int {
	if strings.EqualFold(strings.TrimSpace(title), needle) {
		return 2000
	}
	return 1000
}

// excerpt returns the match with a little text either side, on one line and cut
// at word boundaries so it reads as a phrase rather than a fragment.
func excerpt(text string, idx int, needle string) string {
	if idx < 0 {
		return ""
	}
	r := []rune(text)
	start := idx
	// idx is a byte offset; convert by counting runes up to it.
	start = len([]rune(text[:idx]))
	end := start + len([]rune(needle))

	lo := start - contextRadius
	if lo < 0 {
		lo = 0
	} else {
		for lo < start && !unicode.IsSpace(r[lo]) {
			lo++
		}
	}
	hi := end + contextRadius
	if hi > len(r) {
		hi = len(r)
	} else {
		for hi > end && !unicode.IsSpace(r[hi-1]) {
			hi--
		}
	}

	out := strings.Join(strings.Fields(string(r[lo:hi])), " ")
	if lo > 0 {
		out = "…" + out
	}
	if hi < len(r) {
		out += "…"
	}
	return out
}
