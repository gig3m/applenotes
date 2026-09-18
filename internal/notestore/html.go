package notestore

import (
	"fmt"
	"html"
	"regexp"
	"strings"
)

// ToHTML converts Markdown into the HTML dialect Notes.app accepts.
//
// Notes normalises whatever it is given, so this aims at what survives rather
// than at pretty markup. Two behaviours are load-bearing and were established
// against a real library:
//
//   - Notes takes a note's title from the first line of the body. Setting the
//     name property as well as a title line produces the title twice, so
//     callers pass the title as the body's first line and nothing else.
//   - Adjacent <ul> and <ol> elements are merged into one list, so a separator
//     block is emitted between them.
func ToHTML(md string) string {
	var b strings.Builder
	// Trailing blank lines are not content. Emitting them adds an empty
	// paragraph that the next read renders back, so a note would grow by a line
	// on every round trip.
	// NUL and other C0 controls cannot travel in argv -- os/exec rejects an
	// argument containing NUL outright -- and none of them are note content.
	md = strings.Map(func(r rune) rune {
		if r == 0 || (r < 0x20 && r != '\n' && r != '\t' && r != '\r') {
			return -1
		}
		return r
	}, md)
	// Only line terminators are trimmed. Trimming spaces here as well would
	// take them off the last line, which is content.
	md = strings.TrimRight(md, "\n")
	if i := strings.LastIndexByte(md, '\n'); i >= 0 && strings.TrimSpace(md[i+1:]) == "" {
		// A whitespace-only last line is a leftover terminator, not a
		// paragraph; emitting it adds a blank line to the note.
		md = md[:i]
	}
	lines := strings.Split(md, "\n")

	lists := &listWriter{b: &b}
	inFence := false
	fence := ""

	closeList := func() { lists.closeAll() }

	for _, raw := range lines {
		// Only the line terminator is removed here. The block patterns below
		// match against the marker and hand back the rest of the line intact,
		// so whitespace inside the item is content and survives.
		line := strings.TrimRight(raw, "\r\n")
		trimmed := strings.TrimLeft(line, " \t")

		// Fenced code: emitted verbatim in a monospaced div, since Notes has no
		// block-level code construct reachable through HTML. A fence marker is
		// only ever consumed when it opens or closes a block -- one that does
		// neither is content, and dropping it silently deleted note text.
		if m := fenceRe.FindStringSubmatch(strings.TrimRight(trimmed, " \t")); m != nil {
			marker := m[1]
			if marker == "" {
				marker = m[2]
			}
			if !inFence {
				inFence, fence = true, marker
				closeList()
				continue
			}
			if marker[0] == fence[0] && len(marker) >= len(fence) {
				inFence, fence = false, ""
				continue
			}
		}
		if inFence {
			fmt.Fprintf(&b, "<div><font face=\"Menlo\">%s</font></div>", escapeVerbatim(line))
			continue
		}

		if strings.TrimSpace(line) == "" {
			closeList()
			b.WriteString("<div><br></div>")
			continue
		}

		if m := headingRe.FindStringSubmatch(trimmed); m != nil {
			closeList()
			// h3 and below carry no point size, so Notes stores them as plain
			// bold and they read back as **text** rather than a heading.
			// Clamping to h2 is lossy but stable across a round trip.
			level := len(m[1])
			if level > 2 {
				level = 2
			}
			fmt.Fprintf(&b, "<h%d>%s</h%d>", level, inline(m[2]), level)
			continue
		}

		if m := quoteRe.FindStringSubmatch(trimmed); m != nil {
			closeList()
			// Notes drops <blockquote> entirely, taking the text with it, so
			// the marker is kept as literal text instead. This also protects an
			// ordinary line like ">= 5" that merely looks like a quote.
			fmt.Fprintf(&b, "<div>%s</div>", inline(trimmed))
			continue
		}

		if m := bulletRe.FindStringSubmatch(trimmed); m != nil {
			text := m[2]
			// Notes cannot be given a checklist through HTML, so a task item
			// degrades to a bullet carrying its box as text rather than
			// silently losing the state.
			if c := checkRe.FindStringSubmatch(text); c != nil {
				mark := "☐ "
				if strings.EqualFold(c[1], "x") {
					mark = "☑ "
				}
				text = mark + c[2]
			}
			// "+" carries Notes' dash list; "-" and "*" its dotted one. The
			// class is what Notes keys on -- measured; nothing else produces a
			// dash list through HTML.
			kind := listKind{tag: "ul"}
			if m[1] == "+" {
				kind.class = "Apple-dash-list"
			}
			lists.item(listDepth(raw), kind, inline(text))
			continue
		}

		if m := numberRe.FindStringSubmatch(trimmed); m != nil {
			lists.item(listDepth(raw), listKind{tag: "ol"}, inline(m[1]))
			continue
		}

		closeList()
		fmt.Fprintf(&b, "<div>%s</div>", inline(line))
	}
	closeList()
	out := b.String()
	if out == "" && strings.TrimSpace(md) != "" {
		// Never turn non-blank Markdown into nothing: a caller would overwrite
		// a note with an empty body.
		out = "<div>" + html.EscapeString(strings.TrimSpace(md)) + "</div>"
	}
	return out
}

var (
	// One separator only. A greedy \s+ would swallow the item's own leading
	// whitespace, which is content.
	headingRe = regexp.MustCompile(`^(#{1,6})[ \t](.*)$`)
	bulletRe  = regexp.MustCompile(`^([-*+])[ \t](.*)$`)
	numberRe  = regexp.MustCompile(`^\d+[.)][ \t](.*)$`)
	quoteRe   = regexp.MustCompile(`^>[ \t]?(.*)$`)
	checkRe   = regexp.MustCompile(`^\[([ xX])\][ \t](.*)$`)
	fenceRe   = regexp.MustCompile("^(`{3,})[ \\t]*[^`\\s]*$|^(~{3,})[ \\t]*[^~\\s]*$")

	linkRe   = regexp.MustCompile(`\[([^\]]*)\]\(`)
	codeRe   = regexp.MustCompile("`+")
	strongRe = regexp.MustCompile(`\*\*\*([^*]+)\*\*\*|___([^_]+)___`)
	boldRe   = regexp.MustCompile(`\*\*([^*]+)\*\*|(^|[^_\w])__([^_]+)__`)
	italicRe = regexp.MustCompile(`(^|[^*\w])\*([^*]+)\*|(^|[^_\w])_([^_]+)_`)
	strikeRe = regexp.MustCompile(`~~([^~]+)~~`)

	// Schemes a note may link to. Anything else is emitted as text rather than
	// as a live destination.
	allowedSchemes = []string{"http://", "https://", "mailto:", "tel:", "applenotes:", "message:", "file://"}
)

// inline converts a line's inline Markdown to HTML.
//
// The line is escaped once, up front, by escapeInline -- which also resolves
// Markdown backslash escapes into character references. Everything downstream
// therefore works on already-escaped text and never escapes again. An earlier
// version escaped first and unescaped last, so the emphasis patterns never saw
// the backslashes and silently deleted the characters they were protecting.
func inline(s string) string { return inlineRaw(s, true) }

// inlineRaw resolves code spans and links on unescaped text, escaping each
// fragment in the way its context requires: a code span verbatim, everything
// else with backslash escapes resolved. Escaping the whole line up front would
// strip a code span's backslashes and decode its character references, which is
// exactly the bug the fence path had.
//
// Code spans win over links: CommonMark gives them precedence, so a link
// written inside backticks is literal text.
func inlineRaw(s string, atEnd bool) string {
	var b strings.Builder
	for {
		code := findCodeSpan(s)
		tag := findSupSub(s)
		link := findLink(s)
		// A code span wins ties: CommonMark gives backticks precedence, so
		// <sup> or a link written inside them is literal text.
		switch {
		case code == nil && tag == nil && link == nil:
			b.WriteString(emphasis(escapeFragment(s, atEnd)))
			return b.String()
		case tag != nil && (code == nil || tag[0] < code[0]) && (link == nil || tag[0] < link[0]):
			b.WriteString(emphasis(escapeFragment(s[:tag[0]], false)))
			name := s[tag[2]:tag[3]]
			// The body is inlined in turn, so emphasis and links inside a
			// superscript still convert.
			b.WriteString("<" + name + ">" + inlineRaw(s[tag[4]:tag[5]], false) + "</" + name + ">")
			s = s[tag[1]:]
		case link == nil || (code != nil && code[0] < link[0]):
			b.WriteString(emphasis(escapeFragment(s[:code[0]], false)))
			body := s[code[1]:code[2]]
			// CommonMark strips one space from each end only when both are
			// present and the content is not all spaces.
			if strings.HasPrefix(body, " ") && strings.HasSuffix(body, " ") && strings.TrimSpace(body) != "" {
				body = body[1 : len(body)-1]
			}
			b.WriteString(`<font face="Menlo">` + escapeVerbatim(body) + "</font>")
			s = s[code[3]:]
		default:
			text := s[link[2]:link[3]]
			dest, rest, ok := splitDestination(s[link[1]:])
			if !ok {
				b.WriteString(emphasis(escapeFragment(s[:link[0]+1], false)))
				s = s[link[0]+1:]
				continue
			}
			b.WriteString(emphasis(escapeFragment(s[:link[0]], false)))
			b.WriteString(renderLink(text, dest))
			s = rest
		}
	}
}

// supSubRe matches the inline HTML the renderer emits for a superscript or
// subscript run. Only these two elements are passed through: everything else a
// note contains that looks like a tag is escaped, and the renderer backslashes
// any literal "<" it emits, so a note whose text really says "<sup>" cannot be
// turned into a superscript by a round trip.
var supSubRe = regexp.MustCompile(`(?s)<(sup|sub)>(.*?)</(sup|sub)>`)

// findSupSub locates the next <sup> or <sub>, returning
// [start, end, nameStart, nameEnd, bodyStart, bodyEnd].
func findSupSub(s string) []int {
	for at := 0; at < len(s); {
		m := supSubRe.FindStringSubmatchIndex(s[at:])
		if m == nil {
			return nil
		}
		// FindStringSubmatchIndex cannot require the closing name to match the
		// opening one, so mismatches like <sup>x</sub> are skipped as text.
		if s[at+m[2]:at+m[3]] == s[at+m[6]:at+m[7]] {
			return []int{at + m[0], at + m[1], at + m[2], at + m[3], at + m[4], at + m[5]}
		}
		at += m[1]
	}
	return nil
}

// findCodeSpan locates the next code span, returning the offsets of the opening
// run, the body, and the end of the closing run. Backtick runs must match in
// length, per CommonMark, and a backslash-escaped backtick is literal text.
func findCodeSpan(s string) []int {
	openStart, openEnd := backtickRun(s, 0)
	if openStart < 0 {
		return nil
	}
	ticks := s[openStart:openEnd]
	for i := openEnd; i < len(s); {
		closeStart, closeEnd := backtickRun(s, i)
		if closeStart < 0 {
			break
		}
		if s[closeStart:closeEnd] == ticks {
			return []int{openStart, openEnd, closeStart, closeEnd}
		}
		i = closeEnd
	}
	return nil // an unmatched backtick is literal text
}

// backtickRun finds the next run of unescaped backticks at or after i.
func backtickRun(s string, i int) (start, end int) {
	for ; i < len(s); i++ {
		if s[i] == '\\' {
			i++ // the next byte is literal
			continue
		}
		if s[i] != '`' {
			continue
		}
		start = i
		for i < len(s) && s[i] == '`' {
			i++
		}
		return start, i
	}
	return -1, -1
}

// findLink finds the next unescaped "[...](", returning the offsets of the
// bracket, the text, and the byte after the opening paren.
func findLink(s string) []int {
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] != '[' {
			continue
		}
		for j := i + 1; j < len(s); j++ {
			if s[j] == '\\' {
				j++
				continue
			}
			if s[j] == '[' {
				break // nested; not a link this package emits
			}
			if s[j] != ']' {
				continue
			}
			if j+1 < len(s) && s[j+1] == '(' {
				return []int{i, j + 2, i + 1, j}
			}
			break
		}
	}
	return nil
}

// splitDestination reads a link destination up to its matching close paren,
// allowing balanced parens inside it so a Wikipedia-style URL survives.
func splitDestination(s string) (dest, rest string, ok bool) {
	// An angle-bracketed destination runs to its closing bracket and may
	// contain anything, including spaces.
	if strings.HasPrefix(s, "<") {
		if end := strings.Index(s, ">"); end >= 0 && end+1 < len(s) && s[end+1] == ')' {
			return s[1:end], s[end+2:], true
		}
		return "", "", false
	}
	depth := 1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[:i], s[i+1:], true
			}
		case ' ', '\t':
			return "", "", false
		}
	}
	return "", "", false
}

func renderLink(text, dest string) string {
	// Link text is never the end of a line, so a trailing space inside it stays
	// an ordinary space.
	label := inlineRaw(text, false)
	dest = escapeInline(dest)
	for _, scheme := range allowedSchemes {
		if strings.HasPrefix(strings.ToLower(dest), scheme) {
			return `<a href="` + dest + `">` + label + "</a>"
		}
	}
	// An unrecognised scheme -- javascript:, data:, or a bare word -- is not
	// made clickable. The text is preserved so nothing is lost.
	return label + " (" + dest + ")"
}

// escapeInline HTML-escapes a line and resolves Markdown backslash escapes in
// the same pass. An escaped delimiter becomes a character reference, so the
// emphasis patterns below cannot match it and the character still renders.
func escapeInline(s string) string { return escapeFragment(s, true) }

// escapeFragment escapes one piece of a line. atEnd says whether the piece ends
// the line: a trailing space must become non-breaking only there, or a space
// before an inline code span would too.
func escapeFragment(s string, atEnd bool) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if n := writeSpaceAt(&b, s, i, atEnd); n > 0 {
			continue
		}
		if c == '\\' && i+1 < len(s) && isMarkdownPunct(s[i+1]) {
			i++
			fmt.Fprintf(&b, "&#%d;", s[i])
			continue
		}
		switch c {
		case '&':
			// A character reference the renderer emitted -- &#32; for an
			// indent, say -- is passed through so it still renders.
			if n := entityLen(s[i:]); n > 0 {
				b.WriteString(s[i : i+n])
				i += n - 1
				continue
			}
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&#34;")
		case '\'':
			b.WriteString("&#39;")
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// writeSpace emits a space or tab in a form HTML will not collapse, and
// reports whether it consumed the byte. A leading or trailing space, and the
// second and later space of a run, must be non-breaking: a numeric reference to
// U+0020 does not help, because it is still a space by the time layout runs.
func writeSpace(b *strings.Builder, s string, i int) int {
	return writeSpaceAt(b, s, i, true)
}

func writeSpaceAt(b *strings.Builder, s string, i int, atEnd bool) int {
	switch s[i] {
	case ' ':
		if i == 0 || (atEnd && i == len(s)-1) || s[i-1] == ' ' || s[i-1] == '\t' {
			b.WriteString("&#160;")
			return 1
		}
	case '\t':
		b.WriteString("&#160;&#160;&#160;&#160;")
		return 1
	}
	return 0
}

// escapeVerbatim escapes text that must not be interpreted at all -- the body
// of a fenced block -- while still keeping its whitespace. It deliberately does
// not resolve backslash escapes: inside a fence a backslash is a backslash.
func escapeVerbatim(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if n := writeSpace(&b, s, i); n > 0 {
			continue
		}
		switch s[i] {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&#34;")
		case '\'':
			b.WriteString("&#39;")
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func isMarkdownPunct(c byte) bool {
	return strings.IndexByte("\\`*_[]<>~#+-.!()&{}|=\"'", c) >= 0
}

// entityLen returns the length of a character reference at the start of s, or 0.
func entityLen(s string) int {
	if !strings.HasPrefix(s, "&") {
		return 0
	}
	for i := 1; i < len(s) && i < 34; i++ {
		c := s[i]
		if c == ';' {
			if i == 1 {
				return 0
			}
			return i + 1
		}
		if c == '#' && i == 1 {
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return 0
		}
	}
	return 0
}

func emphasis(s string) string {
	s = strongRe.ReplaceAllString(s, "<b><i>$1$2</i></b>")
	s = boldRe.ReplaceAllString(s, "$2<b>$1$3</b>")
	s = italicRe.ReplaceAllString(s, "$1$3<i>$2$4</i>")
	s = strikeRe.ReplaceAllString(s, "<s>$1</s>")
	return s
}

// listWriter emits nested lists in the one shape Notes accepts.
//
// Measured against Notes on macOS rather than assumed: a nested <ul> inside the
// <li> it hangs off comes back with IndentAmount set, while margin-left on a
// flat <li> is discarded outright and the item lands at the top level. So the
// parent's </li> has to be held open until its child list closes, which is why
// this keeps a stack instead of writing each item whole.
type listWriter struct {
	b     *strings.Builder
	kinds []listKind // the list open at each level
	open  []bool     // whether an <li> is still open at that level
}

// listKind is a list element and the class that distinguishes Notes' two
// bullet styles. Comparable, so two levels can be checked for sameness.
type listKind struct {
	tag   string
	class string
}

func (k listKind) openTag() string {
	if k.class == "" {
		return "<" + k.tag + ">"
	}
	return "<" + k.tag + " class=" + k.class + ">"
}

func (k listKind) closeTag() string { return "</" + k.tag + ">" }

// item writes one list item at the given depth, opening and closing whatever
// levels that implies.
func (w *listWriter) item(depth int, kind listKind, html string) {
	for len(w.kinds) > depth+1 {
		w.closeLevel()
	}
	// A bullet where a numbered list was, at the same depth, is a different
	// list rather than a continuation of this one.
	if len(w.kinds) == depth+1 && w.kinds[depth] != kind {
		w.closeLevel()
		if depth == 0 {
			// Notes merges two adjacent top-level lists unless something
			// separates them.
			w.b.WriteString("<div><br></div>")
		}
	}
	for len(w.kinds) < depth+1 {
		// A deeper list has to sit inside an <li>. Text indented under nothing
		// -- a list that starts indented, or a jump of two levels -- has no
		// parent item to nest in, so one is opened to hold it.
		if n := len(w.kinds); n > 0 && !w.open[n-1] {
			w.b.WriteString("<li>")
			w.open[n-1] = true
		}
		w.b.WriteString(kind.openTag())
		w.kinds = append(w.kinds, kind)
		w.open = append(w.open, false)
	}
	if w.open[depth] {
		w.b.WriteString("</li>")
		w.open[depth] = false
	}
	fmt.Fprintf(w.b, "<li>%s", html)
	w.open[depth] = true
}

func (w *listWriter) closeLevel() {
	d := len(w.kinds) - 1
	if d < 0 {
		return
	}
	if w.open[d] {
		w.b.WriteString("</li>")
		w.open[d] = false
	}
	w.b.WriteString(w.kinds[d].closeTag())
	w.kinds = w.kinds[:d]
	w.open = w.open[:d]
	// The <li> hosting this list stays open: the next item at that level, or
	// closing that level, writes its </li>.
}

func (w *listWriter) closeAll() {
	for len(w.kinds) > 0 {
		w.closeLevel()
	}
}

// listDepth reads the nesting level off a line's leading whitespace. The
// Markdown this round-trips against is written by Markdown(), which indents
// four spaces per level; a tab counts as one level so hand-written notes work
// too.
func listDepth(line string) int {
	cols := 0
	for _, r := range line {
		switch r {
		case ' ':
			cols++
		case '\t':
			cols += 4
		default:
			return cols / 4
		}
	}
	return cols / 4
}
