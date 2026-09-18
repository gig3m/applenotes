package notestore

import (
	"errors"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
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
		// Lowered once and kept: the match offset is found in this string, so
		// every later use of it has to be against this string too. Measuring in
		// one and slicing the other is the bug that made search panic, because
		// strings.ToLower preserves rune count but not byte length.
		lowered := strings.ToLower(text)
		idx := strings.Index(lowered, needle)
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
			h.Context = excerpt(text, runeOffset(lowered, idx), needle)
			if idx < 0 {
				h.Context = n.Title
			}
		default:
			// The count is capped so that a long note mentioning the phrase
			// many times cannot climb past the title band: "titles rank above
			// bodies" has to hold for every note, not just short ones.
			h.Score = 100 + min(strings.Count(lowered, needle), 99)
			h.Context = excerpt(text, runeOffset(lowered, idx), needle)
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

// runeOffset converts a byte offset into the string it was found in to a rune
// offset, which is what excerpt indexes by. Taking a byte offset from one
// string and applying it to another is only safe in ASCII.
func runeOffset(s string, byteIdx int) int {
	if byteIdx < 0 {
		return -1
	}
	return utf8.RuneCountInString(s[:byteIdx])
}

// excerpt returns the match with a little text either side, on one line and cut
// at word boundaries so it reads as a phrase rather than a fragment. start is a
// rune offset into text.
func excerpt(text string, start int, needle string) string {
	if start < 0 {
		return ""
	}
	r := []rune(text)
	if start > len(r) {
		return ""
	}
	end := min(start+len([]rune(needle)), len(r))

	lo := start - contextRadius
	if lo < 0 {
		lo = 0
	} else {
		lo = trimToWord(r, lo, start, +1)
	}
	hi := end + contextRadius
	if hi > len(r) {
		hi = len(r)
	} else {
		hi = trimToWord(r, hi, end, -1)
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

// trimToWord moves an excerpt edge onto a word boundary, walking from edge
// towards limit. Scripts that do not put spaces between words -- Japanese and
// Chinese among them -- have no boundary to find, and walking the whole way
// would leave the match with no context at all, which is the one thing the
// excerpt exists to provide. So the walk gives up halfway and keeps the raw
// edge: a phrase cut mid-word still tells the user which note this is.
func trimToWord(r []rune, edge, limit, dir int) int {
	give := (limit - edge) * dir
	if give < 0 {
		give = -give
	}
	give /= 2
	moved := 0
	for i := edge; i != limit && moved < give; i += dir {
		at := i
		if dir < 0 {
			at = i - 1
		}
		if unicode.IsSpace(r[at]) {
			return i
		}
		moved++
	}
	if moved >= give {
		return edge // no boundary within reach; an uncut edge beats no context
	}
	return limit
}
