package notestore

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"math"
	"strings"
	"testing"
)

// --- tiny protobuf encoder, test-only -------------------------------------

func varint(v uint64) []byte {
	var b []byte
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func tag(num int, wt wireType) []byte { return varint(uint64(num)<<3 | uint64(wt)) }

func fVarint(num int, v uint64) []byte { return append(tag(num, wireVarint), varint(v)...) }

func fBytes(num int, data []byte) []byte {
	out := append(tag(num, wireBytes), varint(uint64(len(data)))...)
	return append(out, data...)
}

// run builds an AttributeRun. style < -1 means "no paragraph_style at all",
// which is how plain body text is normally encoded.
func run(length int, fontWeight int, style int, link string) []byte {
	b := fVarint(1, uint64(length))
	if style >= -1 {
		b = append(b, fBytes(2, fVarint(1, uint64(int32(style))))...)
	}
	if fontWeight != 0 {
		b = append(b, fVarint(5, uint64(fontWeight))...)
	}
	if link != "" {
		b = append(b, fBytes(9, []byte(link))...)
	}
	return b
}

func runIndent(length, style, indent int) []byte {
	ps := append(fVarint(1, uint64(int32(style))), fVarint(4, uint64(indent))...)
	return append(fVarint(1, uint64(length)), fBytes(2, ps)...)
}

func runFont(length int, name string) []byte {
	return append(fVarint(1, uint64(length)), fBytes(3, fBytes(1, []byte(name)))...)
}

// runHeading builds a bold run at a given point size: how Notes stores a
// heading created through HTML.
func runHeading(length int, size float32) []byte {
	font := append(fBytes(1, []byte("Helvetica")), fFixed32(2, math.Float32bits(size))...)
	b := append(fVarint(1, uint64(length)), fBytes(3, font)...)
	return append(b, fVarint(5, uint64(FontBold))...)
}

func fFixed32(num int, v uint32) []byte {
	b := tag(num, wireFixed32)
	return binary.LittleEndian.AppendUint32(b, v)
}

func runCheck(length int, done bool) []byte {
	d := uint64(0)
	if done {
		d = 1
	}
	chk := append(fBytes(1, []byte("uuid")), fVarint(2, d)...)
	ps := append(fVarint(1, uint64(int32(StyleChecklist))), fBytes(5, chk)...)
	return append(fVarint(1, uint64(length)), fBytes(2, ps)...)
}

// runCheckStyleOnly sets only ParagraphStyle.style_type.
func runCheckStyleOnly(length int) []byte {
	style := int32(StyleChecklist)
	return append(fVarint(1, uint64(length)), fBytes(2, fVarint(1, uint64(style)))...)
}

// runCheckFieldOnly sets only ParagraphStyle.checklist, leaving style_type at
// its default.
func runCheckFieldOnly(length int) []byte {
	chk := append(fBytes(1, []byte("uuid")), fVarint(2, 0)...)
	return append(fVarint(1, uint64(length)), fBytes(2, fBytes(5, chk))...)
}

func runQuote(length int) []byte {
	style := int32(StyleBody) // via a variable: int32(-1) is rejected as an untyped constant
	ps := append(fVarint(1, uint64(style)), fVarint(8, 1)...)
	return append(fVarint(1, uint64(length)), fBytes(2, ps)...)
}

func runAttach(length int, id, uti string) []byte {
	ai := append(fBytes(1, []byte(id)), fBytes(2, []byte(uti))...)
	return append(fVarint(1, uint64(length)), fBytes(12, ai)...)
}

func blob(text string, runs ...[]byte) []byte {
	note := fBytes(2, []byte(text))
	for _, r := range runs {
		note = append(note, fBytes(5, r)...)
	}
	doc := append(fVarint(2, 1), fBytes(3, note)...)
	raw := fBytes(2, doc)

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(raw)
	zw.Close()
	return buf.Bytes()
}

func decode(t *testing.T, b []byte) *Note {
	t.Helper()
	n, err := Decode(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return n
}

// --- regression tests ------------------------------------------------------

// A hostile length must not overflow the offset arithmetic and panic.
func TestHostileRunLengthDoesNotPanic(t *testing.T) {
	for name, length := range map[string]uint64{
		"maxint64":       0x7FFFFFFFFFFFFFFF, // narrows to int32 -1
		"maxint32":       0x7FFFFFFF,         // stays large and positive
		"negative int32": 0x80000000,
	} {
		t.Run(name, func(t *testing.T) {
			n, err := Decode(blob("ab", run(1, 0, -2, ""), fVarint(1, length)))
			if err != nil {
				return // rejecting it outright is also fine
			}
			_ = n.Markdown() // must not panic
		})
	}
}

func TestFontWeightIsAStyleEnum(t *testing.T) {
	// Confirmed against a real library: 1=bold, 2=italic, 3=bold+italic.
	for _, tc := range []struct {
		weight int
		want   string
	}{
		{FontDefault, "x"},
		{FontBold, "**x**"},
		{FontItalic, "*x*"},
		{FontBoldItalic, "***x***"},
	} {
		got := decode(t, blob("x", run(1, tc.weight, -2, ""))).Markdown()
		if got != tc.want {
			t.Errorf("font_weight=%d: got %q want %q", tc.weight, got, tc.want)
		}
	}
}

func TestLinksArePreserved(t *testing.T) {
	got := decode(t, blob("ref", run(3, 0, -2, "https://example.com/a;b"))).Markdown()
	want := "[ref](https://example.com/a;b)"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestURLWithSpaceIsWrapped(t *testing.T) {
	// Once percent-encoded the destination needs no <> wrapper.
	got := decode(t, blob("docs", run(4, 0, -2, "https://x.test/a b"))).Markdown()
	if want := "[docs](https://x.test/a%20b)"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// Whitespace outside the " \t" class must survive; NBSP is common in Notes.
func TestExoticWhitespaceSurvives(t *testing.T) {
	const nbsp = " "
	got := decode(t, blob("hello"+nbsp, run(6, FontBold, -2, ""))).Markdown()
	if !strings.Contains(got, nbsp) {
		t.Errorf("NBSP was dropped: %q", got)
	}
}

func TestBodyTextIsNotReinterpretedAsMarkup(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"# not a heading", "\\# not a heading"},
		{"- not a bullet", "\\- not a bullet"},
		{"1986. What a year", "1986\\. What a year"},
		{"    indented", "&#160;   indented"},
		{"  \tindented", "&#160; \tindented"},
		{"Smith & Jones", "Smith & Jones"},
		{"literal &#32; here", "literal \\&#32; here"},
		{"~~~", "\\~\\~\\~"},
	} {
		got := decode(t, blob(tc.text, run(len(tc.text), 0, -2, ""))).Markdown()
		if got != tc.want {
			t.Errorf("%q: got %q want %q", tc.text, got, tc.want)
		}
	}
}

// Text past the last run must not vanish.
func TestUncoveredTailIsNotDropped(t *testing.T) {
	got := decode(t, blob("hello\nworld", run(6, 0, -2, ""))).Markdown()
	if !strings.Contains(got, "world") {
		t.Errorf("tail dropped: %q", got)
	}
}

// A run beginning with a newline must not push its style onto the line above.
func TestParagraphStyleDoesNotBleedBackward(t *testing.T) {
	got := decode(t, blob("abc\ndef", run(3, 0, -2, ""), run(4, 0, StyleDotList, ""))).Markdown()
	want := "abc\n- def"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// Apple encodes a paragraph's terminating newline as its own run carrying that
// paragraph's style. Such a run must attach to the line it terminates and not
// to the line that follows. Taken verbatim from a real note.
func TestLoneNewlineRunDoesNotBleedForward(t *testing.T) {
	got := decode(t, blob("Goals\nPurpose",
		run(5, 0, StyleHeading, ""),
		run(1, 0, StyleHeading, ""),
		run(7, 0, StyleDotList, ""))).Markdown()
	want := "## Goals\n- Purpose"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestOrderedListNumbering(t *testing.T) {
	for name, sep := range map[string]int{"separator as body": StyleBody, "separator as list item": StyleNumList} {
		t.Run("blank line neither consumes a number nor restarts, "+name, func(t *testing.T) {
			got := decode(t, blob("a\n\nb",
				run(2, 0, StyleNumList, ""), run(1, 0, sep, ""), run(1, 0, StyleNumList, ""))).Markdown()
			want := "1. a\n\n2. b"
			if got != want {
				t.Errorf("got %q want %q", got, want)
			}
		})
	}
	t.Run("ending a list ends its nested levels", func(t *testing.T) {
		got := decode(t, blob("a\nb\nc\nd",
			runIndent(2, StyleNumList, 0), runIndent(2, StyleNumList, 1),
			runIndent(2, StyleBody, 0), runIndent(1, StyleNumList, 1))).Markdown()
		want := "1. a\n    1. b\nc\n    1. d"
		if got != want {
			t.Errorf("got %q want %q", got, want)
		}
	})
	t.Run("nested list numbers independently", func(t *testing.T) {
		got := decode(t, blob("a\nb\nc",
			runIndent(2, StyleNumList, 0), runIndent(2, StyleNumList, 1), runIndent(1, StyleNumList, 0))).Markdown()
		want := "1. a\n    1. b\n2. c"
		if got != want {
			t.Errorf("got %q want %q", got, want)
		}
	})
}

func TestMonospaceIsFenced(t *testing.T) {
	got := decode(t, blob("foo *bar*", run(9, 0, StyleMonospace, ""))).Markdown()
	want := "```\nfoo *bar*\n```"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// Nothing inside a fence is markup. A link or bold run in a monospaced
// paragraph must not inject [](...) or ** into the code block.
func TestFenceContentIsVerbatim(t *testing.T) {
	got := decode(t, blob("http://x", run(8, FontBold, StyleMonospace, "http://x"))).Markdown()
	want := "```\nhttp://x\n```"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// A monospaced paragraph containing a fence must not break out of its own.
func TestFenceIsSizedToContent(t *testing.T) {
	got := decode(t, blob("```go", run(5, 0, StyleMonospace, ""))).Markdown()
	want := "````\n```go\n````"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// An inline code span is literal: its content must not be backslash-escaped,
// and the delimiter grows to clear any backticks inside.
//
// The monospaced run is part of a line here, not all of it. A line that is
// monospaced end to end is a block instead -- see
// TestAFullyMonospacedLineIsABlock -- so testing this with a whole line would
// be testing the other thing.
func TestInlineCodeSpanIsNotEscaped(t *testing.T) {
	// Padding is only required when the content itself starts or ends with a
	// backtick; "a`b" does not, so no spaces are added.
	got := decode(t, blob("x a`b", run(2, 0, StyleBody, ""), runFont(3, "Menlo-Regular"))).Markdown()
	if want := "x ``a`b``"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
	padded := decode(t, blob("x `x", run(2, 0, StyleBody, ""), runFont(2, "Menlo-Regular"))).Markdown()
	if want := "x `` `x ``"; padded != want {
		t.Errorf("got %q want %q", padded, want)
	}
}

// A line that is monospaced from end to end is a block, however it got that
// way -- by Notes' Monospaced paragraph style, or by every run on it carrying a
// monospace font.
//
// The two must agree, because a rewrite turns the first into the second: Notes
// will not accept its Monospaced style back through HTML, and the font is what
// survives. Rendering them differently made a note flip from a block to inline
// backticks on its first save.
func TestAFullyMonospacedLineIsABlock(t *testing.T) {
	byStyle := decode(t, blob("names", run(5, 0, StyleMonospace, ""))).Markdown()
	byFont := decode(t, blob("names", runFont(5, "Menlo-Regular"))).Markdown()
	if byStyle != byFont {
		t.Errorf("the two forms disagree:\n  style: %q\n  font:  %q", byStyle, byFont)
	}
	if !strings.HasPrefix(byFont, "```") {
		t.Errorf("a fully monospaced line is not a block: %q", byFont)
	}
}

func TestChecklistRendering(t *testing.T) {
	got := decode(t, blob("todo\ndone",
		runCheck(5, false), runCheck(4, true))).Markdown()
	want := "- [ ] todo\n- [x] done"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestAttachmentIsRendered(t *testing.T) {
	got := decode(t, blob("\uFFFC", runAttach(1, "ABC 123", "public.jpeg"))).Markdown()
	want := "[public.jpeg](applenotes:attachment/ABC%20123)"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// Whitespace is illegal in a Markdown destination even inside <>, so it is
// percent-encoded rather than passed through.
func TestURLWithNewlineIsEncoded(t *testing.T) {
	got := decode(t, blob("t", run(1, 0, -2, "http://x/a\nb"))).Markdown()
	if strings.Contains(got, "\n") {
		t.Errorf("raw newline survived in destination: %q", got)
	}
	if !strings.Contains(got, "%0A") {
		t.Errorf("newline not percent-encoded: %q", got)
	}
}

// RFC 3986 percent-encodes UTF-8 octets. Encoding the code point would turn
// U+2028 into "%2028", which decodes as a space followed by "28".
func TestURLNonASCIIWhitespaceEncodesUTF8(t *testing.T) {
	for _, tc := range []struct{ url, want string }{
		{"http://x/a\u00a0b", "http://x/a%C2%A0b"},
		{"http://x/a\u2028b", "http://x/a%E2%80%A8b"},
	} {
		got := decode(t, blob("t", run(1, 0, -2, tc.url))).Markdown()
		if want := "[t](" + tc.want + ")"; got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
}

// A corrupt run table must stop the walk rather than skip a run without
// advancing pos, which would misalign every run after it.
func TestNegativeLengthEmitsTailUnstyled(t *testing.T) {
	got := decode(t, blob("abcd",
		run(1, FontBold, -2, ""), fVarint(1, 0xFFFFFFFF), run(1, FontItalic, -2, ""))).Markdown()
	if want := "**a**bcd"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// A tilde fence is core CommonMark, so an unescaped "~~~" line would swallow
// every following line of the note into a code block.
func TestTildeFenceIsEscaped(t *testing.T) {
	got := decode(t, blob("~~~\nsecret", run(4, 0, -2, ""), run(6, 0, -2, ""))).Markdown()
	if strings.HasPrefix(got, "~~~") {
		t.Errorf("tilde fence not escaped: %q", got)
	}
}

// Runs often differ only in attributes the renderer ignores. If adjacent spans
// are not merged, a character reference can straddle the boundary and escape
// escaping.
func TestEntitySplitAcrossRunsIsEscaped(t *testing.T) {
	got := decode(t, blob("&amp;", run(1, 0, -2, ""), run(4, 0, -2, ""))).Markdown()
	if want := "\\&amp;"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// A backslash in a destination escapes the next character, and in the wrapped
// form would escape the closing bracket and destroy the link.
func TestBackslashInURLIsEncoded(t *testing.T) {
	got := decode(t, blob("t", run(1, 0, -2, "https://x/(a\\"))).Markdown()
	if strings.Contains(got, "\\") {
		t.Errorf("raw backslash survived in destination: %q", got)
	}
}

func TestOversizedBlobRejected(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(make([]byte, MaxDecompressed+1))
	zw.Close()
	if _, err := Decode(buf.Bytes()); err != ErrTooLarge {
		t.Errorf("got %v want ErrTooLarge", err)
	}
}

// An emoji costs two UTF-16 units; styling must stay aligned across it.
func TestUTF16RunAlignment(t *testing.T) {
	got := decode(t, blob("\U0001F600ok", run(2, FontBold, -2, ""), run(2, 0, -2, ""))).Markdown()
	want := "**\U0001F600**ok"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestVarintOverflowRejected(t *testing.T) {
	overflowing := []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x7F}
	err := scan(append(tag(1, wireVarint), overflowing...), func(field) error { return nil })
	if err == nil {
		t.Fatal("expected overflow to be rejected")
	}
}

func TestWireTypeMismatchIsRejected(t *testing.T) {
	// AttributeRun.length arriving length-delimited must error, not read as 0
	// and silently misalign every later run.
	bad := fBytes(1, []byte("xx"))
	if _, err := decodeRun(bad); err == nil {
		t.Fatal("expected wire-type mismatch to be rejected")
	}
}

func FuzzDecode(f *testing.F) {
	f.Add(blob("hello", run(5, FontBold, -2, "https://x.test")))
	f.Add(blob("a\nb", run(2, 0, StyleNumList, ""), run(1, 0, StyleDotList, "")))
	f.Fuzz(func(t *testing.T, data []byte) {
		n, err := Decode(data)
		if err != nil {
			return
		}
		_ = n.Markdown()
		_ = n.Title()
		_ = n.PlainText()
	})
}

// A link split across runs -- by a code span, a bold word, anything the
// renderer styles differently -- is still one link, not several.
func TestLinkSpanningSeveralRunsIsOneLink(t *testing.T) {
	const url = "https://x.test/docs"
	got := decode(t, blob("the foo bar",
		run(4, FontDefault, -2, url),
		runFontLink(4, "Menlo-Regular", url),
		run(3, FontDefault, -2, url))).Markdown()
	want := "[the `foo` bar](" + url + ")"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func runFontLink(length int, name, link string) []byte {
	b := append(fVarint(1, uint64(length)), fBytes(3, fBytes(1, []byte(name)))...)
	return append(b, fBytes(9, []byte(link))...)
}

// Destroys names content a rewrite would lose outright, so a caller can refuse.
func TestDestroysDetectsContentLoss(t *testing.T) {
	for _, tc := range []struct {
		name string
		runs [][]byte
		want string
	}{
		{"attachment", [][]byte{runAttach(1, "ID", "public.jpeg")}, "attachments"},
		// Two fixtures, because a checklist can be encoded either way and one
		// fixture setting both satisfies two independent guards at once.
		{"checklist by style", [][]byte{runCheckStyleOnly(4)}, "checklists"},
		{"checklist by field", [][]byte{runCheckFieldOnly(4)}, "checklists"},
	} {
		if got := decode(t, blob("text", tc.runs...)).Destroys(); !contains(got, tc.want) {
			t.Errorf("%s: got %v, want it to include %q", tc.name, got, tc.want)
		}
	}
}

// Four paragraph styles with no output assertion at all until now.
func TestParagraphStylePrefixes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		style int
		want  string
	}{
		{"title", StyleTitle, "# text"},
		{"heading", StyleHeading, "## text"},
		{"subheading", StyleSubhead, "### text"},
		{"dash list", StyleDashList, "+ text"},
		{"dot list", StyleDotList, "- text"},
	} {
		got := decode(t, blob("text", run(4, 0, tc.style, ""))).Markdown()
		if got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestBlockQuotePrefix(t *testing.T) {
	if got, want := decode(t, blob("text", runQuote(4))).Markdown(), "> text"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// A heading is recovered from an enlarged point size only when the line is also
// bold. Without the bold requirement, any enlarged body text is promoted.
func TestEnlargedButNotBoldIsNotAHeading(t *testing.T) {
	got := decode(t, blob("text", runSizedNotBold(4, 24))).Markdown()
	if strings.HasPrefix(got, "#") {
		t.Errorf("promoted enlarged non-bold text to a heading: %q", got)
	}
}

func runSizedNotBold(length int, size float32) []byte {
	font := append(fBytes(1, []byte("Helvetica")), fFixed32(2, math.Float32bits(size))...)
	return append(fVarint(1, uint64(length)), fBytes(3, font)...)
}

// An attachment whose submessage failed to decode still occupies its slot in
// the text. The guard must not fail open just because the metadata is
// unreadable.
func TestUndecodableAttachmentIsStillDestructive(t *testing.T) {
	n := decode(t, blob("￼", run(1, 0, -2, "")))
	if got := n.Destroys(); len(got) == 0 {
		t.Error("an attachment with no decodable metadata was not reported")
	}
}

// Degrades names formatting that would flatten. It must never block a write, so
// it is reported separately from Destroys.
func TestDegradesDetectsFormattingLoss(t *testing.T) {
	for _, tc := range []struct {
		name string
		runs [][]byte
		want string
	}{
		{"subheading", [][]byte{run(4, 0, StyleSubhead, "")}, "subheadings"},
		{"block quote", [][]byte{runQuote(4)}, "block quotes"},
		// Indentation degrades only where it is really lost. A list keeps its
		// nesting through a rewrite now; an ordinary paragraph does not,
		// because Notes discards margin-left.
		{"indent on a paragraph", [][]byte{runIndent(4, StyleBody, 1)}, "indentation"},
	} {
		if got := decode(t, blob("text", tc.runs...)).Degrades(); !contains(got, tc.want) {
			t.Errorf("%s: got %v, want it to include %q", tc.name, got, tc.want)
		}
		// None of these may block a write.
		if got := decode(t, blob("text", tc.runs...)).Destroys(); len(got) != 0 {
			t.Errorf("%s: formatting reported as destructive: %v", tc.name, got)
		}
	}
}

// Underline rides along with hyperlinks in real notes -- a quarter of the link
// runs in a real library carry it -- so it must not make a linked note
// unwritable.
func TestUnderlinedLinkIsNotDestructive(t *testing.T) {
	n := decode(t, blob("link", runUnderlineLink(4, "https://x.test")))
	if got := n.Destroys(); len(got) != 0 {
		t.Errorf("an underlined link blocked the write: %v", got)
	}
}

// Ordinary prose is safe to rewrite.
func TestPlainNoteIsNeitherDestructiveNorDegraded(t *testing.T) {
	n := decode(t, blob("title\nbody", run(6, 0, StyleTitle, ""), run(4, FontBold, -2, "https://x.test")))
	if got := n.Destroys(); len(got) != 0 {
		t.Errorf("plain note reported destructive: %v", got)
	}
	if got := n.Degrades(); len(got) != 0 {
		t.Errorf("plain note reported degraded: %v", got)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func runUnderline(length int) []byte {
	return append(fVarint(1, uint64(length)), fVarint(6, 1)...)
}

func runUnderlineLink(length int, link string) []byte {
	b := append(fVarint(1, uint64(length)), fVarint(6, 1)...)
	return append(b, fBytes(9, []byte(link))...)
}

// An attachment run that also carries a link must not be folded into the link
// group: doing so nested one set of brackets inside another.
func TestAttachmentInsideLinkIsNotGrouped(t *testing.T) {
	const url = "https://x.test/d"
	got := decode(t, blob("a￼b",
		run(1, 0, -2, url),
		runAttachLink(1, "ID", "public.jpeg", url),
		run(1, 0, -2, url))).Markdown()
	// The attachment ends the link group, so the text either side is linked
	// separately. What must not happen is the attachment's own brackets being
	// nested inside the surrounding link.
	// The invariant, not a hand-picked substring: no "[" may appear between a
	// link's opening bracket and its "](", or one link is nested in another.
	for i := 0; i < len(got); i++ {
		if got[i] != '[' {
			continue
		}
		if close := strings.Index(got[i+1:], "]("); close >= 0 {
			if strings.ContainsRune(got[i+1:i+1+close], '[') {
				t.Errorf("nested link brackets: %q", got)
				break
			}
		}
	}
	if !strings.Contains(got, "applenotes:attachment/ID") {
		t.Errorf("attachment lost: %q", got)
	}
}

func runAttachLink(length int, id, uti, link string) []byte {
	ai := append(fBytes(1, []byte(id)), fBytes(2, []byte(uti))...)
	b := append(fVarint(1, uint64(length)), fBytes(12, ai)...)
	return append(b, fBytes(9, []byte(link))...)
}
