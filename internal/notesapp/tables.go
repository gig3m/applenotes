package notesapp

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"strings"

	"github.com/gig3m/applenotes/internal/applescript"
)

// Tables.
//
// A table is stored as an attachment whose contents are a CRDT in its own
// protobuf -- rows, columns and cells held as three separate ordered
// structures. It is decodable, and the cell text comes out readily, but the
// mapping from the cell map's row keys to the row ordering tree is a second
// format again.
//
// Notes will simply hand the table over as HTML. Asked for a note's body it
// returns ordinary <table><tr><td>, already in display order, so that is what
// this uses. One Apple Event, and only for a note that actually contains a
// table -- two notes in a real library of fifty.
//
// The cost is that AppleScript drops hyperlink hrefs, which is why reads come
// from the database everywhere else. Inside a table cell that is a real loss,
// and the alternative is having no table at all.

const bodyScript = `on run argv
	tell application "Notes" to return body of note id (item 1 of argv)
end run`

var (
	tablePlaceholder = regexp.MustCompile(`\[com\.apple\.notes\.table\]\(applenotes:attachment/[^)]*\)`)
	tableBlock       = regexp.MustCompile(`(?is)<table.*?</table>`)
	rowBlock         = regexp.MustCompile(`(?is)<tr.*?</tr>`)
	cellBlock        = regexp.MustCompile(`(?is)<t[dh][^>]*>(.*?)</t[dh]>`)
	tagStripper      = regexp.MustCompile(`(?s)<[^>]*>`)
)

// FillTables replaces each table placeholder in md with the table itself.
//
// Best effort: if Notes cannot be asked, or returns fewer tables than the note
// has placeholders, the placeholders that cannot be filled are left alone. A
// note that renders with one table missing is better than one that fails to
// render.
func FillTables(ctx context.Context, run applescript.Runner, scriptID, md string) string {
	if !tablePlaceholder.MatchString(md) {
		return md
	}
	body, err := run.Run(ctx, bodyScript, scriptID)
	if err != nil {
		return md
	}
	tables := tableBlock.FindAllString(body, -1)
	i := 0
	return tablePlaceholder.ReplaceAllStringFunc(md, func(placeholder string) string {
		if i >= len(tables) {
			return placeholder
		}
		out := markdownTable(tables[i])
		i++
		if out == "" {
			return placeholder
		}
		return out
	})
}

// markdownTable converts one <table> to a Markdown table.
//
// Markdown requires a header row, and Notes' tables do not have the concept --
// every row is a row. The first is used as the header because that is what it
// nearly always is, and the alternative is an empty header line above the
// data, which reads worse and is no more honest.
func markdownTable(t string) string {
	var rows [][]string
	width := 0
	for _, r := range rowBlock.FindAllString(t, -1) {
		var cells []string
		for _, c := range cellBlock.FindAllStringSubmatch(r, -1) {
			cells = append(cells, cellText(c[1]))
		}
		if len(cells) == 0 {
			continue
		}
		if len(cells) > width {
			width = len(cells)
		}
		rows = append(rows, cells)
	}
	if len(rows) == 0 || width == 0 {
		return ""
	}

	var b strings.Builder
	for i, r := range rows {
		b.WriteString("|")
		for c := 0; c < width; c++ {
			cell := ""
			if c < len(r) {
				cell = r[c]
			}
			fmt.Fprintf(&b, " %s |", cell)
		}
		b.WriteString("\n")
		if i == 0 {
			b.WriteString("|")
			for c := 0; c < width; c++ {
				b.WriteString(" --- |")
			}
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// cellText flattens a cell to one line.
//
// A cell can hold several paragraphs, and a newline inside a Markdown table
// cell ends the table. <br> and block ends become spaces rather than being
// dropped, so two paragraphs do not run together into one word.
func cellText(s string) string {
	s = regexp.MustCompile(`(?i)<br\s*/?>|</div>|</p>`).ReplaceAllString(s, " ")
	s = tagStripper.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = strings.Join(strings.Fields(s), " ")
	// A pipe would end the cell early; a backslash escape is how Markdown
	// carries one.
	return strings.ReplaceAll(s, "|", `\|`)
}
