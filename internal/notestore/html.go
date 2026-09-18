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
		if r == 0 || (r < 0x20 && r != '\n' && r != '\t') {
			return -1
		}
		return r
	}, md)
	lines := strings.Split(strings.TrimRight(md, "\n \t"), "\n")

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

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

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

		if trimmed == "" {
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
			fmt.Fprintf(&b, "<div>&gt; %s</div>", inline(m[1]))
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
		fmt.Fprintf(&b, "<div>%s</div>", inline(trimmed))
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
	headingRe = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	bulletRe  = regexp.MustCompile(`^([-*+])\s+(.*)$`)
	numberRe  = regexp.MustCompile(`^\d+[.)]\s+(.*)$`)
	quoteRe   = regexp.MustCompile(`^>\s?(.*)$`)
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
// Links are resolved first and code spans second, each by splitting the string
// rather than substituting placeholders. An earlier version reserved sentinel
// strings in the text; note content could contain the sentinel, and a code span
// inside link text left one behind unreplaced.
func inline(s string) string {
	var b strings.Builder
	for {
		loc := linkRe.FindStringSubmatchIndex(s)
		if loc == nil {
			break
		}
		text := s[loc[2]:loc[3]]
		dest, rest, ok := splitDestination(s[loc[1]:])
		if !ok {
			// Not a link after all: emit the "[" and carry on from just after
			// it so the rest of the line is still processed.
			b.WriteString(inlineNoLinks(s[:loc[0]+1]))
			s = s[loc[0]+1:]
			continue
		}
		b.WriteString(inlineNoLinks(s[:loc[0]]))
		b.WriteString(renderLink(text, dest))
		s = rest
	}
	b.WriteString(inlineNoLinks(s))
	return b.String()
}

// splitDestination reads a link destination up to its matching close paren,
// allowing balanced parens inside it so a Wikipedia-style URL survives.
func splitDestination(s string) (dest, rest string, ok bool) {
	// An angle-bracketed destination runs to its closing bracket and may
	// contain anything, including spaces.
	if strings.HasPrefix(s, "<") {
		if end := strings.Index(s, ">"); end >= 0 && end+1 < len(s) && s[end+1] == ')' {
			return s[:end+1], s[end+2:], true
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
	label := inlineNoLinks(text)
	// The renderer wraps a destination containing parens or whitespace in angle
	// brackets, per CommonMark. Unwrap it so such a link survives a round trip.
	if strings.HasPrefix(dest, "<") && strings.HasSuffix(dest, ">") {
		dest = dest[1 : len(dest)-1]
	}
	dest = unescapeMarkdown(dest)
	for _, scheme := range allowedSchemes {
		if strings.HasPrefix(strings.ToLower(dest), scheme) {
			return "<a href=\"" + html.EscapeString(dest) + "\">" + label + "</a>"
		}
	}
	// An unrecognised scheme -- javascript:, data:, or a bare word -- is not
	// made clickable. The text is preserved so nothing is lost.
	return label + " (" + html.EscapeString(dest) + ")"
}

// inlineNoLinks handles everything except links: code spans are split out so
// their contents are never read as emphasis, then escaped and emitted verbatim.
func inlineNoLinks(s string) string {
	var b strings.Builder
	for {
		open := codeRe.FindStringIndex(s)
		if open == nil {
			break
		}
		ticks := s[open[0]:open[1]]
		after := s[open[1]:]
		closeIdx := -1
		for _, m := range codeRe.FindAllStringIndex(after, -1) {
			if after[m[0]:m[1]] == ticks {
				closeIdx = m[0]
				break
			}
		}
		if closeIdx < 0 {
			break // an unmatched backtick is literal text
		}
		b.WriteString(emphasis(s[:open[0]]))
		body := after[:closeIdx]
		body = strings.TrimPrefix(body, " ")
		body = strings.TrimSuffix(body, " ")
		b.WriteString("<font face=\"Menlo\">" + html.EscapeString(body) + "</font>")
		s = after[closeIdx+len(ticks):]
	}
	b.WriteString(emphasis(s))
	return b.String()
}

func emphasis(s string) string {
	s = html.EscapeString(s)
	s = strongRe.ReplaceAllString(s, "<b><i>$1$2</i></b>")
	s = boldRe.ReplaceAllString(s, "$2<b>$1$3</b>")
	s = italicRe.ReplaceAllString(s, "$1$3<i>$2$4</i>")
	s = strikeRe.ReplaceAllString(s, "<s>$1</s>")
	return unescapeMarkdown(s)
}

// unescapeMarkdown turns backslash escapes back into their literal character,
// so text this package's own renderer escaped survives a round trip.
func unescapeMarkdown(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("\\`*_[]<>~#+-.!()&", s[i+1]) >= 0 {
			i++
			b.WriteByte(s[i])
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
