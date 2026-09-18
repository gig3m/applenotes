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
		// block-level code construct reachable through HTML.
		if m := fenceRe.FindStringSubmatch(trimmed); m != nil {
			if !inFence {
				inFence, fence = true, m[1]
				closeList()
			} else if strings.HasPrefix(m[1], fence[:1]) && len(m[1]) >= len(fence) {
				inFence, fence = false, ""
			}
			continue
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
			level := len(m[1])
			if level > 3 {
				level = 3
			}
			fmt.Fprintf(&b, "<h%d>%s</h%d>", level, inline(m[2]), level)
			continue
		}

		if m := quoteRe.FindStringSubmatch(trimmed); m != nil {
			closeList()
			fmt.Fprintf(&b, "<blockquote><div>%s</div></blockquote>", inline(m[1]))
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
	return b.String()
}

var (
	headingRe = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	bulletRe  = regexp.MustCompile(`^([-*+])\s+(.*)$`)
	numberRe  = regexp.MustCompile(`^\d+[.)]\s+(.*)$`)
	quoteRe   = regexp.MustCompile(`^>\s?(.*)$`)
	checkRe   = regexp.MustCompile(`^\[([ xX])\]\s+(.*)$`)
	fenceRe   = regexp.MustCompile("^(`{3,}|~{3,})\\s*\\w*$")

	linkRe   = regexp.MustCompile(`\[([^\]]*)\]\(([^)\s]+)\)`)
	codeRe   = regexp.MustCompile("`+[^`]*`+")
	strongRe = regexp.MustCompile(`\*\*\*([^*]+)\*\*\*|___([^_]+)___`)
	boldRe   = regexp.MustCompile(`\*\*([^*]+)\*\*|__([^_]+)__`)
	italicRe = regexp.MustCompile(`(^|[^*\w])\*([^*]+)\*|(^|[^_\w])_([^_]+)_`)
	strikeRe = regexp.MustCompile(`~~([^~]+)~~`)
)

// inline converts a line's inline Markdown. Code spans are extracted first so
// their contents are never treated as emphasis, then restored at the end.
func inline(s string) string {
	var code []string
	s = codeRe.ReplaceAllStringFunc(s, func(m string) string {
		body := strings.Trim(m, "`")
		body = strings.TrimPrefix(body, " ")
		body = strings.TrimSuffix(body, " ")
		code = append(code, body)
		return fmt.Sprintf("\x00CODE%d\x00", len(code)-1)
	})

	var links [][2]string
	s = linkRe.ReplaceAllStringFunc(s, func(m string) string {
		p := linkRe.FindStringSubmatch(m)
		links = append(links, [2]string{p[1], p[2]})
		return fmt.Sprintf("\x00LINK%d\x00", len(links)-1)
	})

	s = html.EscapeString(s)
	s = strongRe.ReplaceAllString(s, "<b><i>$1$2</i></b>")
	s = boldRe.ReplaceAllString(s, "<b>$1$2</b>")
	s = italicRe.ReplaceAllString(s, "$1$3<i>$2$4</i>")
	s = strikeRe.ReplaceAllString(s, "<s>$1</s>")
	s = unescapeMarkdown(s)

	for i, c := range code {
		s = strings.Replace(s, fmt.Sprintf("\x00CODE%d\x00", i),
			"<font face=\"Menlo\">"+html.EscapeString(c)+"</font>", 1)
	}
	for i, l := range links {
		s = strings.Replace(s, fmt.Sprintf("\x00LINK%d\x00", i),
			fmt.Sprintf("<a href=%q>%s</a>", html.EscapeString(l[1]), inline(l[0])), 1)
	}
	return s
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
