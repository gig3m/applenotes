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
	b := blob("ab", run(1, 0, -2, ""), fVarint(1, 0x7FFFFFFFFFFFFFFF))
	n, err := Decode(b)
	if err != nil {
		return // rejecting it outright is also fine
	}
	_ = n.Markdown() // must not panic
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
	got := decode(t, blob("docs", run(4, 0, -2, "https://x.test/a b"))).Markdown()
	if !strings.Contains(got, "(<https://x.test/a%20b>)") {
		t.Errorf("space in URL not escaped: %q", got)
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
	for _, text := range []string{"# not a heading", "- not a bullet", "1986. What a year"} {
		got := decode(t, blob(text, run(len(text), 0, -2, ""))).Markdown()
		if got == text {
			t.Errorf("unescaped structure: %q", got)
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
	t.Run("blank item consumes no number", func(t *testing.T) {
		got := decode(t, blob("a\n\nb",
			run(2, 0, StyleNumList, ""), run(1, 0, StyleNumList, ""), run(1, 0, StyleNumList, ""))).Markdown()
		if strings.Contains(got, "3.") {
			t.Errorf("blank item consumed a number: %q", got)
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
	if !strings.HasPrefix(got, "```") || !strings.Contains(got, "foo *bar*") {
		t.Errorf("monospace not fenced verbatim: %q", got)
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
