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

	listKind := "" // "ul", "ol", or ""
	inFence := false
	fence := ""

	closeList := func() {
		if listKind != "" {
			fmt.Fprintf(&b, "</%s>", listKind)
			listKind = ""
		}
	}
	openList := func(kind string) {
		if listKind == kind {
			return
		}
		if listKind != "" {
			closeList()
			// Two lists in a row are merged by Notes unless something
			// separates them.
			b.WriteString("<div><br></div>")
		}
		fmt.Fprintf(&b, "<%s>", kind)
		listKind = kind
	}

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
		if m := fenceRe.FindStringSubmatch(trimmed); m != nil {
			if !inFence {
				inFence, fence = true, m[1]
				closeList()
				continue
			}
			if m[1][0] == fence[0] && len(m[1]) >= len(fence) {
				inFence, fence = false, ""
				continue
			}
		}
		if inFence {
			fmt.Fprintf(&b, "<div><font face=\"Menlo\">%s</font></div>", html.EscapeString(line))
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
			openList("ul")
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
			fmt.Fprintf(&b, "<li>%s</li>", inline(text))
			continue
		}

		if m := numberRe.FindStringSubmatch(trimmed); m != nil {
			openList("ol")
			fmt.Fprintf(&b, "<li>%s</li>", inline(m[1]))
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
	checkRe   = regexp.MustCompile(`^\[([ xX])\]\s+(.*)$`)
	fenceRe   = regexp.MustCompile("^(`{3,}|~{3,})\\s*\\S*$")

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
func inline(s string) string {
	return inlineEscaped(escapeInline(s))
}

// inlineEscaped resolves code spans and links, in that order of precedence.
// Code spans win: CommonMark gives them precedence over links, so a link
// written inside backticks is literal text.
func inlineEscaped(s string) string {
	var b strings.Builder
	for {
		code := findCodeSpan(s)
		link := linkRe.FindStringSubmatchIndex(s)
		switch {
		case code == nil && link == nil:
			b.WriteString(emphasis(s))
			return b.String()
		case link == nil || (code != nil && code[0] < link[0]):
			b.WriteString(emphasis(s[:code[0]]))
			body := strings.TrimSuffix(strings.TrimPrefix(s[code[1]:code[2]], " "), " ")
			b.WriteString(`<font face="Menlo">` + body + "</font>")
			s = s[code[3]:]
		default:
			text := s[link[2]:link[3]]
			dest, rest, ok := splitDestination(s[link[1]:])
			if !ok {
				b.WriteString(emphasis(s[:link[0]+1]))
				s = s[link[0]+1:]
				continue
			}
			b.WriteString(emphasis(s[:link[0]]))
			b.WriteString(renderLink(text, dest))
			s = rest
		}
	}
}

// findCodeSpan locates the next code span, returning the offsets of the opening
// run, the body, and the end of the closing run. Backtick runs must match in
// length, per CommonMark.
func findCodeSpan(s string) []int {
	open := codeRe.FindStringIndex(s)
	if open == nil {
		return nil
	}
	ticks := s[open[0]:open[1]]
	after := s[open[1]:]
	for _, m := range codeRe.FindAllStringIndex(after, -1) {
		if after[m[0]:m[1]] == ticks {
			return []int{open[0], open[1], open[1] + m[0], open[1] + m[1]}
		}
	}
	return nil // an unmatched backtick is literal text
}

// splitDestination reads a link destination up to its matching close paren,
// allowing balanced parens inside it so a Wikipedia-style URL survives.
func splitDestination(s string) (dest, rest string, ok bool) {
	// An angle-bracketed destination runs to its closing bracket and may
	// contain anything, including spaces.
	if strings.HasPrefix(s, "&lt;") {
		if end := strings.Index(s, "&gt;"); end >= 0 && end+4 < len(s) && s[end+4] == ')' {
			return s[4:end], s[end+5:], true
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
	label := inlineEscaped(text)
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
func escapeInline(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' && (i == 0 || i == len(s)-1 || s[i-1] == ' ' || s[i-1] == '\t') {
			// A leading or trailing space, or the second and later space of a
			// run, would be collapsed away. Only a non-breaking space survives; a numeric
			// reference to U+0020 does not, because it is still a space by the
			// time layout runs.
			b.WriteString("&#160;")
			continue
		}
		if c == '\t' {
			b.WriteString("&#160;&#160;&#160;&#160;")
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
