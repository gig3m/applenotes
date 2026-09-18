package notestore

import (
	"strings"
	"testing"
)

func TestToHTMLBlocks(t *testing.T) {
	for _, tc := range []struct{ name, md, want string }{
		{"paragraph", "hello", "<div>hello</div>"},
		{"heading", "## Title", "<h2>Title</h2>"},
		{"deep heading clamps", "##### Deep", "<h3>Deep</h3>"},
		{"bullets", "- a\n- b", "<ul><li>a</li><li>b</li></ul>"},
		{"numbered", "1. a\n2. b", "<ol><li>a</li><li>b</li></ol>"},
		{"blank line", "a\n\nb", "<div>a</div><div><br></div><div>b</div>"},
		{"quote", "> q", "<blockquote><div>q</div></blockquote>"},
	} {
		if got := ToHTML(tc.md); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

// Notes merges two adjacent lists into one unless something separates them.
func TestToHTMLSeparatesAdjacentLists(t *testing.T) {
	got := ToHTML("- a\n1. b")
	want := "<ul><li>a</li></ul><div><br></div><ol><li>b</li></ol>"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestToHTMLInline(t *testing.T) {
	for _, tc := range []struct{ name, md, want string }{
		{"bold", "**x**", "<div><b>x</b></div>"},
		{"italic", "*x*", "<div><i>x</i></div>"},
		{"bold italic", "***x***", "<div><b><i>x</i></b></div>"},
		{"strike", "~~x~~", "<div><s>x</s></div>"},
		{"link", "[t](https://x.test/a)", `<div><a href="https://x.test/a">t</a></div>`},
		{"code", "`a`", `<div><font face="Menlo">a</font></div>`},
	} {
		if got := ToHTML(tc.md); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

// A code span is literal: emphasis markers inside it are text.
func TestToHTMLCodeSpanIsNotEmphasised(t *testing.T) {
	got := ToHTML("`a*b*c`")
	if strings.Contains(got, "<i>") {
		t.Errorf("emphasis applied inside a code span: %q", got)
	}
}

// HTML metacharacters in note text must not become markup.
func TestToHTMLEscapesText(t *testing.T) {
	got := ToHTML("a < b & c > d")
	if strings.Contains(got, "<div>a < b") {
		t.Errorf("unescaped: %q", got)
	}
	if !strings.Contains(got, "&lt;") || !strings.Contains(got, "&amp;") {
		t.Errorf("expected escaping: %q", got)
	}
}

// Text the renderer escaped must come back as its literal character, so a
// round trip does not accumulate backslashes.
func TestToHTMLUnescapesMarkdown(t *testing.T) {
	got := ToHTML(`\# not a heading`)
	if !strings.Contains(got, "<div># not a heading</div>") {
		t.Errorf("got %q", got)
	}
}

// Trailing blank lines would add an empty paragraph that the next read renders
// back, growing the note on every round trip.
func TestToHTMLDropsTrailingBlankLines(t *testing.T) {
	if got, want := ToHTML("a\n\n\n"), "<div>a</div>"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// Notes stores an HTML heading as bold text at an enlarged point size, with no
// paragraph style. Recovering it is what makes a heading survive write-read.
func TestHTMLHeadingIsRecovered(t *testing.T) {
	for _, tc := range []struct {
		size float32
		want string
	}{
		{24, "# Title"},
		{18, "## Title"},
		{12, "**Title**"}, // ordinary bold text, not a heading
	} {
		n := decode(t, blob("Title", runHeading(5, tc.size)))
		if got := n.Markdown(); got != tc.want {
			t.Errorf("size %v: got %q want %q", tc.size, got, tc.want)
		}
	}
}

// A bolded phrase inside a paragraph must not be promoted to a heading.
func TestPartialBoldIsNotAHeading(t *testing.T) {
	n := decode(t, blob("big small", runHeading(4, 18), run(5, FontDefault, -2, "")))
	if got := n.Markdown(); strings.HasPrefix(got, "#") {
		t.Errorf("promoted a partially-bold line to a heading: %q", got)
	}
}
