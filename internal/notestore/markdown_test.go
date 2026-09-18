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
