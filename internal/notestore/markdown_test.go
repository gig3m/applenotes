package notestore

import "testing"

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
