package notestore

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// objectReplacement occupies an attachment's slot in the note text.
const objectReplacement = '￼'

// span is a slice of the note text carrying resolved inline styling.
type span struct {
	text       string
	bold       bool
	italic     bool
	strike     bool
	mono       bool
	link       string
	attachment *AttachmentInfo
}

// Markdown renders a decoded note as Markdown. Paragraph styles become headings
// and list markers; inline runs become emphasis and links. Links are the reason
// this reads the protobuf at all: AppleScript drops them.
func (n *Note) Markdown() string {
	lines := n.lines(n.units())

	var out []string
	counters := map[int]int{}
	inMono := false

	closeMono := func() {
		if inMono {
			out = append(out, "```")
			inMono = false
		}
	}

	for _, ln := range lines {
		style := ln.style()
		indent := ln.indent()
		body := renderSpans(ln.spans, style == StyleMonospace)
		blank := strings.TrimSpace(body) == ""

		// Monospaced paragraphs become a fenced block, and their contents are
		// emitted verbatim -- escaping inside a fence would be visible.
		if style == StyleMonospace {
			if !inMono {
				out = append(out, "```")
				inMono = true
			}
			out = append(out, body)
			continue
		}
		closeMono()

		if blank {
			delete(counters, indent)
			out = append(out, "")
			continue
		}

		if style != StyleNumList {
			delete(counters, indent)
		}
		prefix := ""
		switch style {
		case StyleTitle:
			prefix = "# "
		case StyleHeading:
			prefix = "## "
		case StyleSubhead:
			prefix = "### "
		case StyleDotList, StyleDashList:
			prefix = "- "
		case StyleNumList:
			counters[indent]++
			for d := range counters {
				if d > indent {
					delete(counters, d)
				}
			}
			prefix = fmt.Sprintf("%d. ", counters[indent])
		case StyleChecklist:
			prefix = "- [ ] "
			if c := ln.checklist(); c != nil && c.Done {
				prefix = "- [x] "
			}
		}
		if indent > 0 {
			prefix = strings.Repeat("    ", indent) + prefix
		}
		if ln.blockQuote() {
			prefix = "> " + prefix
		}
		out = append(out, prefix+body)
	}
	closeMono()
	return strings.Join(out, "\n")
}

// PlainText returns the note body with no styling, which is what search and
// previews want.
func (n *Note) PlainText() string { return n.Text }

// Title is the first non-empty line, matching how Notes derives a note's name.
func (n *Note) Title() string {
	for _, l := range strings.Split(n.Text, "\n") {
		if s := strings.TrimSpace(l); s != "" {
			return s
		}
	}
	return ""
}

type line struct {
	spans []span
	runs  []*AttributeRun
}

func (l line) style() int {
	for _, r := range l.runs {
		if r.ParagraphStyle != nil {
			return r.ParagraphStyle.StyleType
		}
	}
	return StyleBody
}

func (l line) indent() int {
	for _, r := range l.runs {
		if r.ParagraphStyle != nil {
			if r.ParagraphStyle.IndentAmount > 0 {
				return r.ParagraphStyle.IndentAmount
			}
			return 0
		}
	}
	return 0
}

func (l line) blockQuote() bool {
	for _, r := range l.runs {
		if r.ParagraphStyle != nil && r.ParagraphStyle.BlockQuote != 0 {
			return true
		}
	}
	return false
}

func (l line) checklist() *Checklist {
	for _, r := range l.runs {
		if r.ParagraphStyle != nil && r.ParagraphStyle.Checklist != nil {
			return r.ParagraphStyle.Checklist
		}
	}
	return nil
}

// lines splits the note into display lines, carrying each line's covering runs
// with it. Run lengths are in UTF-16 units, so the walk is done in that domain
// and decoded back to UTF-8 per slice.
func (n *Note) lines(units []uint16) []line {
	var out []line
	cur := line{}
	pos := 0

	emit := func() {
		out = append(out, cur)
		cur = line{}
	}

	// appendChunk splits one run's text across newlines, attributing each piece
	// to the line it belongs to.
	appendChunk := func(chunk []uint16, r *AttributeRun) {
		allNewlines := true
		for _, u := range chunk {
			if u != '\n' {
				allNewlines = false
				break
			}
		}
		start := 0
		for j := 0; j <= len(chunk); j++ {
			if j == len(chunk) || chunk[j] == '\n' {
				// Attach when the run contributes characters to this line, or
				// when we are sitting on a newline that is the run's entire
				// contribution. The second clause must not fire on the final
				// iteration (j == len(chunk)), or a lone-newline run would also
				// be attached to the following line and bleed its style forward.
				if j > start || (j < len(chunk) && allNewlines) {
					text := string(utf16.Decode(chunk[start:j]))
					if text != "" && r != nil {
						cur.spans = append(cur.spans, spanFor(r, text))
					} else if text != "" {
						cur.spans = append(cur.spans, span{text: text})
					}
					if r != nil {
						cur.runs = append(cur.runs, r)
					}
				}
				if j < len(chunk) {
					emit()
				}
				start = j + 1
			}
		}
	}

	for i := range n.Runs {
		r := &n.Runs[i]
		length := r.Length
		if length <= 0 {
			continue
		}
		// Written as a subtraction so a hostile length cannot overflow the
		// comparison and slip past the clamp.
		if length > len(units)-pos {
			length = len(units) - pos
		}
		if length <= 0 {
			break
		}
		chunk := units[pos : pos+length]
		pos += length
		appendChunk(chunk, r)
	}

	// Text past the last run still belongs to the note; emit it unstyled rather
	// than silently dropping it.
	if pos < len(units) {
		appendChunk(units[pos:], nil)
	}

	if len(cur.spans) > 0 || len(cur.runs) > 0 {
		emit()
	}
	return out
}

func spanFor(r *AttributeRun, text string) span {
	name := strings.ToLower(r.FontName)
	return span{
		text:       text,
		bold:       r.Bold(),
		italic:     r.Italic(),
		strike:     r.Strikethrough,
		mono:       strings.Contains(name, "mono") || strings.Contains(name, "courier"),
		link:       r.Link,
		attachment: r.Attachment,
	}
}

func renderSpans(spans []span, wholeLineMono bool) string {
	var b strings.Builder
	for _, s := range spans {
		t := s.text
		if t == "" {
			continue
		}
		if s.attachment != nil {
			b.WriteString(renderAttachment(s))
			continue
		}
		// lead/trail/core are split with one consistent definition of space, so
		// that lead+core+trail reconstructs the original exactly. Using
		// different cutsets here silently deletes NBSP and \r.
		rest := strings.TrimLeftFunc(t, unicode.IsSpace)
		lead := t[:len(t)-len(rest)]
		core := strings.TrimRightFunc(rest, unicode.IsSpace)
		trail := rest[len(core):]
		if core == "" {
			b.WriteString(t)
			continue
		}

		if !wholeLineMono {
			core = escapeText(core)
		}
		if s.mono && !wholeLineMono {
			core = "`" + core + "`"
		}
		if s.strike {
			core = "~~" + core + "~~"
		}
		if s.bold && s.italic {
			core = "***" + core + "***"
		} else if s.bold {
			core = "**" + core + "**"
		} else if s.italic {
			core = "*" + core + "*"
		}
		if s.link != "" {
			core = "[" + core + "](" + escapeURL(s.link) + ")"
		}
		b.WriteString(lead + core + trail)
	}
	out := b.String()
	if !wholeLineMono {
		out = escapeLineStart(out)
	}
	return out
}

func renderAttachment(s span) string {
	label := "attachment"
	if s.attachment.TypeUTI != "" {
		label = s.attachment.TypeUTI
	}
	body := strings.Map(func(r rune) rune {
		if r == objectReplacement {
			return -1
		}
		return r
	}, s.text)
	if strings.TrimSpace(body) != "" {
		label = strings.TrimSpace(body)
	}
	if s.attachment.Identifier == "" {
		return "[" + escapeText(label) + "]"
	}
	return "[" + escapeText(label) + "](applenotes:attachment/" + s.attachment.Identifier + ")"
}

// escapeText neutralises inline Markdown metacharacters so note text is never
// reinterpreted as markup.
func escapeText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\\', '`', '*', '_', '[', ']', '<', '>':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// escapeLineStart neutralises leading characters that would otherwise turn a
// plain paragraph into a heading, a list item or a quote.
func escapeLineStart(s string) string {
	trimmed := strings.TrimLeftFunc(s, unicode.IsSpace)
	lead := s[:len(s)-len(trimmed)]
	if trimmed == "" {
		return s
	}
	r, size := utf8.DecodeRuneInString(trimmed)
	switch r {
	case '#', '>', '-', '+', '=':
		return lead + "\\" + trimmed
	}
	if r >= '0' && r <= '9' {
		rest := trimmed[size:]
		for len(rest) > 0 && rest[0] >= '0' && rest[0] <= '9' {
			rest = rest[1:]
		}
		if len(rest) > 0 && (rest[0] == '.' || rest[0] == ')') {
			n := len(trimmed) - len(rest)
			return lead + trimmed[:n] + "\\" + rest
		}
	}
	return s
}

// escapeURL renders a link destination safely. Whitespace is not permitted bare
// in a Markdown destination, so such URLs are wrapped in angle brackets.
func escapeURL(u string) string {
	if strings.ContainsFunc(u, unicode.IsSpace) || strings.ContainsAny(u, "()") {
		r := strings.NewReplacer("<", "%3C", ">", "%3E", " ", "%20")
		return "<" + r.Replace(u) + ">"
	}
	return u
}
