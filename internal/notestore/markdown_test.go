package notestore

import (
	"strings"
	"testing"
)

// What Degrades reports was measured against Notes, not assumed, and two of its
// former entries turned out to be wrong.
//
// A nested list survives now -- ToHTML emits real nested <ul>/<ol> and Notes
// stores the indent back -- so reporting it warned about a loss that does not
// happen. Indentation on an ordinary paragraph still does not survive, because
// Notes discards margin-left, so that case has to keep warning.
func TestListNestingNoLongerCountsAsADegrade(t *testing.T) {
	nested := &Note{
		Text: "item\n",
		Runs: []AttributeRun{{
			Length:         5,
			ParagraphStyle: &ParagraphStyle{StyleType: StyleDotList, IndentAmount: 1},
		}},
	}
	for _, f := range nested.Degrades() {
		if f == "indentation" {
			t.Errorf("a nested list was reported as losing its indentation: %v", nested.Degrades())
		}
	}

	// The same indent on a paragraph that is not a list is still lost.
	plain := &Note{
		Text: "item\n",
		Runs: []AttributeRun{{
			Length:         5,
			ParagraphStyle: &ParagraphStyle{StyleType: StyleBody, IndentAmount: 1},
		}},
	}
	if !contains(plain.Degrades(), "indentation") {
		t.Errorf("an indented paragraph reported %v, want indentation", plain.Degrades())
	}
}

// Notes draws hyperlinks underlined and stores that as a real attribute, so
// nearly every note containing a link carried this warning -- and the link
// comes back underlined anyway, because Notes underlines it again. A banner
// that fires on almost everything stops being read, which costs more than it
// saves.
func TestUnderliningIsNotReported(t *testing.T) {
	n := &Note{
		Text: "a link\n",
		Runs: []AttributeRun{{Length: 7, Underlined: true, Link: "https://x.test/"}},
	}
	if contains(n.Degrades(), "underlining") {
		t.Errorf("underlining is still reported: %v", n.Degrades())
	}
}

// runSup builds a run carrying superscript (+1) or subscript (-1).
func runSup(length, sup int) []byte {
	return append(fVarint(1, uint64(length)), fVarint(8, uint64(uint32(int32(sup))))...)
}

// Reading is half the round trip: if the renderer does not emit <sup>, there is
// nothing for the writer to pass through and the styling is lost on the way
// out, with no warning now that it is no longer reported as degrading.
func TestSuperscriptIsRenderedAsInlineHTML(t *testing.T) {
	for _, tc := range []struct {
		name string
		sup  int
		want string
	}{
		{"superscript", 1, "x<sup>2</sup>"},
		{"subscript", -1, "x<sub>2</sub>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := decode(t, blob("x2", run(1, 0, StyleBody, ""), runSup(1, tc.sup)))
			if got := strings.TrimSpace(n.Markdown()); got != tc.want {
				t.Errorf("Markdown() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Runs differing only in superscript must not be merged, or "x2" renders as one
// span and the superscript is lost before it reaches the renderer.
func TestSuperscriptRunsAreNotMergedWithPlainOnes(t *testing.T) {
	n := decode(t, blob("abc",
		run(1, 0, StyleBody, ""),
		runSup(1, 1),
		run(1, 0, StyleBody, "")))
	got := strings.TrimSpace(n.Markdown())
	if got != "a<sup>b</sup>c" {
		t.Errorf("Markdown() = %q, want %q", got, "a<sup>b</sup>c")
	}
}

// Neither is reported as degrading any more, because neither degrades.
func TestSuperscriptNoLongerCountsAsADegrade(t *testing.T) {
	n := decode(t, blob("x2", run(1, 0, StyleBody, ""), runSup(1, 1)))
	for _, f := range n.Degrades() {
		if strings.Contains(f, "superscript") {
			t.Errorf("superscript is still reported: %v", n.Degrades())
		}
	}
}

// A dash list rendered as "- " came back as a dot list with nothing reported.
func TestDashListGetsItsOwnMarker(t *testing.T) {
	dash := decode(t, blob("text", runIndent(4, StyleDashList, 0)))
	if got := strings.TrimSpace(dash.Markdown()); got != "+ text" {
		t.Errorf("dash list rendered %q, want %q", got, "+ text")
	}
	dot := decode(t, blob("text", runIndent(4, StyleDotList, 0)))
	if got := strings.TrimSpace(dot.Markdown()); got != "- text" {
		t.Errorf("dot list rendered %q, want %q", got, "- text")
	}
}

// Notes does not only break lines with U+000A.
//
// A line entered with shift-return, and most text pasted in from elsewhere, is
// separated by U+2028 LINE SEPARATOR, which Notes renders as a line break like
// any other. Splitting on "\n" alone turned a real twenty-five line order of
// service into three lines of run-on text -- it read as one paragraph with
// bullet characters scattered through it, while the same note on an iPhone was
// a clean list.
func TestUnicodeLineSeparatorsBreakLines(t *testing.T) {
	for _, tc := range []struct {
		name string
		sep  string
	}{
		{"newline", "\n"},
		{"line separator", " "},
		{"paragraph separator", " "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := "first" + tc.sep + "second" + tc.sep + "third"
			n := decode(t, blob(text, run(len([]rune(text)), 0, StyleBody, "")))
			got := n.Markdown()
			if lines := strings.Split(strings.TrimSpace(got), "\n"); len(lines) != 3 {
				t.Errorf("got %d lines, want 3: %q", len(lines), got)
			}
			for _, want := range []string{"first", "second", "third"} {
				if !strings.Contains(got, want) {
					t.Errorf("lost %q: %q", want, got)
				}
			}
		})
	}
}

// The title is the first line, by whichever separator ends it -- every one of
// them, not just the common one. A note whose first break is U+2029 would
// otherwise be named after its entire contents.
func TestTitleStopsAtAUnicodeLineSeparator(t *testing.T) {
	for _, sep := range []string{"\n", "\u2028", "\u2029"} {
		text := "Order of worship" + sep + "\u00a0\u00a0\u2022 Call to worship"
		n := decode(t, blob(text, run(len([]rune(text)), 0, StyleBody, "")))
		if got := n.Title(); got != "Order of worship" {
			t.Errorf("separator %q: Title() = %q, want %q", sep, got, "Order of worship")
		}
	}
}

// Styling has to follow the text across the new separators, or a run that
// spans one bleeds its formatting into the line below.
func TestStylingIsNotSmearedAcrossALineSeparator(t *testing.T) {
	text := "bold here plain here"
	n := decode(t, blob(text,
		run(len([]rune("bold here ")), FontBold, StyleBody, ""),
		run(len([]rune("plain here")), 0, StyleBody, "")))
	got := n.Markdown()
	if !strings.Contains(got, "**bold here**") {
		t.Errorf("the bold line lost its emphasis: %q", got)
	}
	if strings.Contains(got, "**plain here**") {
		t.Errorf("emphasis bled onto the next line: %q", got)
	}
}
