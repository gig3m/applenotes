package notestore

import (
	"html"
	"regexp"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestToHTMLBlocks(t *testing.T) {
	for _, tc := range []struct{ name, md, want string }{
		{"paragraph", "hello", "<div>hello</div>"},
		{"heading", "## Title", "<h2>Title</h2>"},
		// h3 and below carry no point size, so Notes stores them as plain bold
		// and they read back as **text**. Clamping to h2 keeps them stable.
		{"deep heading clamps to h2", "##### Deep", "<h2>Deep</h2>"},
		{"bullets", "- a\n- b", "<ul><li>a</li><li>b</li></ul>"},
		{"numbered", "1. a\n2. b", "<ol><li>a</li><li>b</li></ol>"},
		{"blank line", "a\n\nb", "<div>a</div><div><br></div><div>b</div>"},
		// Notes drops <blockquote> and its text with it, so the marker is kept
		// as literal characters instead.
		{"quote keeps its text", "> q", "<div>&gt; q</div>"},
		{"quote-looking text keeps its spacing", ">= 5 is the rule", "<div>&gt;= 5 is the rule</div>"},
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
// A code span inside link text previously left an unreplaced sentinel -- and a
// NUL byte -- in the output, which os/exec rejects outright.
func TestCodeSpanInsideLinkText(t *testing.T) {
	got := ToHTML("[see `foo`](https://x.test)")
	want := `<div><a href="https://x.test">see <font face="Menlo">foo</font></a></div>`
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// Nothing may ever reach argv with a NUL in it.
func TestToHTMLNeverEmitsNUL(t *testing.T) {
	for _, md := range []string{
		"[see `foo`](https://x.test)",
		"\x00CODE0\x00 and `real`",
		"`a` `b` [x](`y`)",
	} {
		if strings.ContainsRune(ToHTML(md), 0) {
			t.Errorf("NUL in output for %q", md)
		}
	}
}

// A fence marker that neither opens nor closes a block is content. Dropping it
// silently deleted note text, and could empty a note entirely.
func TestFenceNeverDeletesContent(t *testing.T) {
	for _, tc := range []struct{ md, mustContain string }{
		{"~~~\n```\n~~~", "```"},
		{"```\n~~~\nkept\n```", "~~~"},
	} {
		got := ToHTML(tc.md)
		if !strings.Contains(got, html.EscapeString(tc.mustContain)) {
			t.Errorf("%q: lost %q, got %q", tc.md, tc.mustContain, got)
		}
	}
}

// Non-blank Markdown must never convert to nothing; a caller would otherwise
// overwrite a note with an empty body.
func TestToHTMLNeverEmptyForNonBlankInput(t *testing.T) {
	for _, md := range []string{"```\n```", "~~~\n```\n~~~", "> ", "#", "-"} {
		if got := ToHTML(md); got == "" {
			t.Errorf("empty HTML for %q", md)
		}
	}
}

// A destination may contain balanced parens.
func TestLinkWithParensInURL(t *testing.T) {
	got := ToHTML("[t](https://en.wikipedia.org/wiki/Go_(programming_language))")
	want := `<div><a href="https://en.wikipedia.org/wiki/Go_(programming_language)">t</a></div>`
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// The renderer emits an angle-wrapped destination when a URL contains parens
// or whitespace; that form must convert back to the same link.
func TestAngleWrappedDestination(t *testing.T) {
	for _, tc := range []struct{ md, want string }{
		{"[t](<https://en.wikipedia.org/wiki/Go_(programming_language)>)",
			`<div><a href="https://en.wikipedia.org/wiki/Go_(programming_language)">t</a></div>`},
		{"[t](<https://x.test/a%20b>)", `<div><a href="https://x.test/a%20b">t</a></div>`},
	} {
		if got := ToHTML(tc.md); got != tc.want {
			t.Errorf("got %q want %q", got, tc.want)
		}
	}
}

// An unrecognised scheme is not made clickable, but its destination text is
// preserved. The assertion is on the destination, not the label: the label is
// emitted on every path, so asserting on it can never fail.
func TestDisallowedSchemeIsNotLinked(t *testing.T) {
	for _, tc := range []struct{ md, dest string }{
		{"[a](javascript:alert(1))", "javascript:alert(1)"},
		{"[a](data:text/html,x)", "data:text/html,x"},
		// The allow-list matches a prefix, not a substring: an allowed scheme
		// appearing later in the destination must not make it live.
		{"[a](x:https://evil)", "x:https://evil"},
	} {
		got := ToHTML(tc.md)
		if strings.Contains(got, "<a href") {
			t.Errorf("%q produced a live link: %q", tc.md, got)
		}
		if !strings.Contains(got, html.EscapeString(tc.dest)) {
			t.Errorf("%q lost its destination text: %q", tc.md, got)
		}
	}
}

// A destination must not be able to break out of the href attribute. Asserting
// on the absence of "<script>" cannot catch this, because the angle brackets
// are escaped by a different branch than the quote.
func TestDestinationCannotEscapeTheAttribute(t *testing.T) {
	got := ToHTML(`[t](https://x.test/a"><script>alert(1)</script>)`)
	if i := strings.Index(got, `href="`); i >= 0 {
		rest := got[i+6:]
		if end := strings.Index(rest, `"`); end >= 0 && strings.Contains(rest[:end], `"`) {
			t.Errorf("raw quote inside href: %q", got)
		}
	}
	if strings.Contains(got, `"><script`) {
		t.Errorf("attribute break-out: %q", got)
	}
}

// Backslash escapes and character references are literal inside a code span,
// exactly as inside a fence. Escaping the line before finding the span
// resolved them and deleted characters.
func TestCodeSpanContentIsVerbatim(t *testing.T) {
	for _, tc := range []struct{ md, want string }{
		{"`a\\*b`", `<div><font face="Menlo">a\*b</font></div>`},
		{"`&amp;`", `<div><font face="Menlo">&amp;amp;</font></div>`},
	} {
		if got := ToHTML(tc.md); got != tc.want {
			t.Errorf("got %q want %q", got, tc.want)
		}
	}
}

// Intraword underscores are not emphasis.
func TestIntrawordUnderscores(t *testing.T) {
	for _, md := range []string{"foo__bar__baz", "snake_case_name", "a_b_c"} {
		if got := ToHTML(md); strings.Contains(got, "<b>") || strings.Contains(got, "<i>") {
			t.Errorf("%q was emphasised: %q", md, got)
		}
	}
}

// Every text path is escaped. These are the call sites a mutation test showed
// were previously unguarded.
func TestEscapingAtEveryCallSite(t *testing.T) {
	for _, tc := range []struct{ name, md string }{
		{"href", `[t](https://x.test/a"><script>alert(1)</script>)`},
		{"code span", "`<script>`"},
		{"fenced code", "```\n<script>\n```"},
		{"link text", "[<script>](https://x.test)"},
		{"body", "<script>"},
	} {
		got := ToHTML(tc.md)
		if strings.Contains(got, "<script>") {
			t.Errorf("%s: unescaped: %q", tc.name, got)
		}
		// A destination must not be able to break out of the href attribute.
		if strings.Contains(got, `"><`) {
			t.Errorf("%s: attribute break-out: %q", tc.name, got)
		}
	}
}

func TestChecklistMarks(t *testing.T) {
	got := ToHTML("- [ ] todo\n- [x] done")
	if !strings.Contains(got, "☐ todo") || !strings.Contains(got, "☑ done") {
		t.Errorf("got %q", got)
	}
}

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
	// An escaped delimiter becomes a character reference: the emphasis
	// patterns cannot match it, and it still renders as the character.
	if got, want := ToHTML(`\# not a heading`), "<div>&#35; not a heading</div>"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// The renderer escapes Markdown metacharacters in note text; the converter must
// give them back unchanged. Escaping first and unescaping last -- the previous
// order -- deleted them outright: "snake\_case\_name" became "snakecasename".
func TestTextSurvivesRenderThenConvert(t *testing.T) {
	for _, text := range []string{
		"snake_case_name", "*star*", "__dunder__", "x`y`z", "[a](b)",
		"**", "a_b_c", "~~x~~", "# not a heading", "1986. what a year",
		"a < b & c > d", "back\\slash", "&#32; literal", "  indented",
		"= not a setext", "=== separator", "a  b", "trailing  ",
		"a<b>c", "]", "&nbsp; literal",
	} {
		// Build a note whose body is exactly this text, render it, convert it
		// back, and check no character was lost.
		md := decode(t, blob(text, run(len(utf16.Encode([]rune(text))), 0, -2, ""))).Markdown()
		html := ToHTML(md)
		// Leading and repeated spaces are emitted as non-breaking spaces: a
		// plain space is collapsed away by HTML, so preserving the text's
		// appearance costs the exact character. Normalise before comparing.
		if got := strings.ReplaceAll(htmlToText(html), " ", " "); got != text {
			t.Errorf("round trip changed %q -> md %q -> %q", text, md, got)
		}
	}
}

// htmlToText recovers the visible characters from the converter's output, so a
// test can assert that nothing was dropped.
func htmlToText(s string) string {
	s = tagRe.ReplaceAllString(s, "")
	return html.UnescapeString(s)
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

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

// Whitespace assertions are made on the HTML, not on a round trip: a round trip
// that unescapes &#160; back to a space cannot tell whether the mechanism is
// there at all, and an earlier version of these tests passed with it deleted.
func TestWhitespaceIsNonBreaking(t *testing.T) {
	for _, tc := range []struct{ name, md, want string }{
		{"interior run", "a  b", "<div>a &#160;b</div>"},
		{"leading", " a", "<div>&#160;a</div>"},
		{"trailing", "a ", "<div>a&#160;</div>"},
		{"tab", "\ta", "<div>&#160;&#160;&#160;&#160;a</div>"},
		{"single interior space untouched", "a b", "<div>a b</div>"},
		// Every block branch, not just the paragraph one: an earlier version
		// matched markers against a TrimSpace'd line and deleted the item's
		// own whitespace.
		{"bullet trailing", "- hello ", "<ul><li>hello&#160;</li></ul>"},
		{"bullet leading", "-   lead", "<ul><li>&#160;&#160;lead</li></ul>"},
		{"heading trailing", "## hello ", "<h2>hello&#160;</h2>"},
		{"numbered trailing", "1. n ", "<ol><li>n&#160;</li></ol>"},
		{"quote trailing", "> q ", "<div>&gt; q&#160;</div>"},
		{"heading leading", "##   lead", "<h2>&#160;&#160;lead</h2>"},
		{"numbered leading", "1.   lead", "<ol><li>&#160;&#160;lead</li></ol>"},
		// A fence body is the sixth block branch, and was the last one still
		// flattening code indentation to column 0.
		{"fence indent", "```\n    if x:\n```", `<div><font face="Menlo">&#160;&#160;&#160;&#160;if x:</font></div>`},
		{"fence interior", "```\na  b\n```", `<div><font face="Menlo">a &#160;b</font></div>`},
		// A backslash inside a fence is a backslash, not an escape.
		{"fence backslash", "```\na\\_b\n```", `<div><font face="Menlo">a\_b</font></div>`},
		{"fence with info string and trailing space", "```go \nx\n```", `<div><font face="Menlo">x</font></div>`},
		// Tab-indented code is the case that matters for Go and Makefiles, and
		// it went through a different branch than the space-indented one.
		{"fence tab indent", "```\n\tif x:\n```", `<div><font face="Menlo">&#160;&#160;&#160;&#160;if x:</font></div>`},
		// A line that is itself a code span is not a fence marker; treating it
		// as one deleted the line.
		{"code span line is not a fence", "```x```\nrest", `<div><font face="Menlo">x</font></div><div>rest</div>`},
		{"checklist separator", "- [ ]   lead", "<ul><li>\u2610 &#160;&#160;lead</li></ul>"},
		{"uppercase checkbox", "- [X] done", "<ul><li>\u2611 done</li></ul>"},
		// No separator after a marker means it is not a marker.
		{"no separator is not a heading", "#head", "<div>#head</div>"},
		{"no separator is not a bullet", "-item", "<div>-item</div>"},
	} {
		if got := ToHTML(tc.md); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

// A whitespace-only final line is a leftover terminator, not a paragraph.
func TestTrailingWhitespaceLineIsNotAParagraph(t *testing.T) {
	if got, want := ToHTML("a\n  \n"), "<div>a</div>"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
