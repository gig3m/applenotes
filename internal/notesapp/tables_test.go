package notesapp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gig3m/applenotes/internal/applescript"
)

func fakeRun(out string, err error) applescript.RunnerFunc {
	return func(context.Context, string, ...string) (string, error) { return out, err }
}

// What Notes actually returns, shortened. Taken from a real note rather than
// invented, because the attributes and the nesting are what the parsing has to
// survive.
const notesHTML = `<div>before</div>
<table cellspacing="0" cellpadding="0" style="border-collapse: collapse; direction: ltr">
<tbody>
<tr><td valign="top" style="border-color: #ccc"><div><b>Quarter</b></div>
</td><td valign="top" style="border-color: #ccc"><div><b>Focus</b></div>
</td><td valign="top" style="border-color: #ccc"><div><b>Core Question</b></div>
</td></tr>
<tr><td valign="top"><div>Q1</div></td><td valign="top"><div>Vision</div>
</td><td valign="top"><div>What is the life Jesus actually offers?</div></td></tr>
</tbody>
</table>
<div>after</div>`

const placeholder = "[com.apple.notes.table](applenotes:attachment/C8A15C3E)"

// A table read as its own attachment placeholder, so a note lost its entire
// contents -- the daemon reported a UTI where iCloud shows a table.
func TestATableBecomesAMarkdownTable(t *testing.T) {
	md := "# Note\n\n" + placeholder + "\n\nafter"
	got := FillTables(context.Background(), fakeRun(notesHTML, nil), "ID", md)

	for _, want := range []string{
		"| Quarter | Focus | Core Question |",
		"| --- | --- | --- |",
		"| Q1 | Vision | What is the life Jesus actually offers? |",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "com.apple.notes.table") {
		t.Errorf("the placeholder survived:\n%s", got)
	}
	// The rest of the note is untouched.
	for _, want := range []string{"# Note", "after"} {
		if !strings.Contains(got, want) {
			t.Errorf("lost %q from around the table", want)
		}
	}
}

// Best effort, every way it can fail. A note that renders with one table
// missing is better than one that fails to render, and the placeholder at
// least says something is there.
func TestAFailedLookupLeavesThePlaceholder(t *testing.T) {
	md := "before " + placeholder + " after"
	for _, tc := range []struct {
		name string
		run  applescript.RunnerFunc
	}{
		{"Notes cannot be asked", fakeRun("", errors.New("not running"))},
		{"the body has no table in it", fakeRun("<div>nothing here</div>", nil)},
		{"the body is empty", fakeRun("", nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := FillTables(context.Background(), tc.run, "ID", md); got != md {
				t.Errorf("got %q, want the input unchanged", got)
			}
		})
	}
}

// A note with no table must not cost an Apple Event. Nearly every note is in
// this case, and the round trip is to a Mac across a tailnet.
func TestANoteWithoutATableAsksNotesNothing(t *testing.T) {
	called := false
	run := applescript.RunnerFunc(func(context.Context, string, ...string) (string, error) {
		called = true
		return "", nil
	})
	FillTables(context.Background(), run, "ID", "# Just a note\n\nwith no table in it")
	if called {
		t.Error("asked Notes for the body of a note with no table")
	}
}

// Two tables in one note have to line up with their own placeholders, in
// order.
func TestTablesFillInOrder(t *testing.T) {
	body := `<table><tr><td>first</td></tr></table><table><tr><td>second</td></tr></table>`
	md := "a " + placeholder + " b " + placeholder + " c"
	got := FillTables(context.Background(), fakeRun(body, nil), "ID", md)
	i, j := strings.Index(got, "first"), strings.Index(got, "second")
	if i < 0 || j < 0 || i > j {
		t.Errorf("tables did not fill in order:\n%s", got)
	}
}

// A cell holding several paragraphs has to become one line: a newline inside a
// Markdown table cell ends the table. A pipe has to be escaped for the same
// reason.
func TestCellsAreFlattenedAndEscaped(t *testing.T) {
	body := `<table><tr><td><div>one</div><div>two</div></td><td>a | b</td></tr></table>`
	got := FillTables(context.Background(), fakeRun(body, nil), "ID", placeholder)
	if strings.Count(got, "\n") > 2 { // the row, and the header rule
		t.Errorf("a multi-paragraph cell broke the table:\n%s", got)
	}
	if !strings.Contains(got, "one two") {
		t.Errorf("paragraphs ran together or were dropped:\n%s", got)
	}
	if !strings.Contains(got, `a \| b`) {
		t.Errorf("an unescaped pipe would end the cell early:\n%s", got)
	}
}

// Entities come back as characters, not as "&amp;".
func TestEntitiesAreDecoded(t *testing.T) {
	body := `<table><tr><td>Tom &amp; Jerry</td></tr></table>`
	got := FillTables(context.Background(), fakeRun(body, nil), "ID", placeholder)
	if !strings.Contains(got, "Tom & Jerry") {
		t.Errorf("entity not decoded:\n%s", got)
	}
}
