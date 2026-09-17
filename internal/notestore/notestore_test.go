package notestore

import (
	"bytes"
	"compress/gzip"
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

func runCheck(length int, done bool) []byte {
	d := uint64(0)
	if done {
		d = 1
	}
	chk := append(fBytes(1, []byte("uuid")), fVarint(2, d)...)
	ps := append(fVarint(1, uint64(int32(StyleChecklist))), fBytes(5, chk)...)
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
	got := decode(t, blob("ref", run(3, 0, -2, "https://ref.ly/Heb13.17;nkjv"))).Markdown()
	want := "[ref](https://ref.ly/Heb13.17;nkjv)"
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
		{"    indented", "&#32;   indented"},
		{"  \tindented", "&#32; \tindented"},
		{"Smith & Jones", "Smith & Jones"},
		{"literal &#32; here", "literal \\&#32; here"},
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
	t.Run("blank line neither consumes a number nor restarts", func(t *testing.T) {
		got := decode(t, blob("a\n\nb",
			run(2, 0, StyleNumList, ""), run(1, 0, StyleBody, ""), run(1, 0, StyleNumList, ""))).Markdown()
		want := "1. a\n\n2. b"
		if got != want {
			t.Errorf("got %q want %q", got, want)
		}
	})
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
func TestInlineCodeSpanIsNotEscaped(t *testing.T) {
	// Padding is only required when the content itself starts or ends with a
	// backtick; "a`b" does not, so no spaces are added.
	got := decode(t, blob("a`b", runFont(3, "Menlo-Regular"))).Markdown()
	if want := "``a`b``"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
	padded := decode(t, blob("`x", runFont(2, "Menlo-Regular"))).Markdown()
	if want := "`` `x ``"; padded != want {
		t.Errorf("got %q want %q", padded, want)
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
