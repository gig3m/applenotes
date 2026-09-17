package notestore

import (
	"fmt"
	"strings"
	"unicode/utf16"
)

const (
	hintItalic = 1
	hintBold   = 2
)

// span is a slice of the note text carrying resolved inline styling.
type span struct {
	text   string
	bold   bool
	italic bool
	strike bool
	mono   bool
	link   string
}

// Markdown renders a decoded note as Markdown. Paragraph styles become headings
// and list markers; inline runs become emphasis and links. Links are the reason
// this reads the protobuf at all: AppleScript drops them.
func (n *Note) Markdown() string {
	units := n.units()
	lines := n.lines(units)

	var b strings.Builder
	numbering := 0
	for i, ln := range lines {
		style := ln.style()
		if style != StyleNumList {
			numbering = 0
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
			numbering++
			prefix = fmt.Sprintf("%d. ", numbering)
		case StyleChecklist:
			prefix = "- [ ] "
			if c := ln.checklist(); c != nil && c.Done {
				prefix = "- [x] "
			}
		}
		if ind := ln.indent(); ind > 0 {
			prefix = strings.Repeat("    ", ind) + prefix
		}
		if ln.blockQuote() {
			prefix = "> " + prefix
		}

		body := renderSpans(ln.spans, style == StyleMonospace)
		if strings.TrimSpace(body) == "" {
			prefix = ""
			body = ""
		}
		b.WriteString(prefix)
		b.WriteString(body)
		if i < len(lines)-1 {
			b.WriteByte('\n')
		}
	}
	_ = units
	return b.String()
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
			return r.ParagraphStyle.IndentAmount
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

	for i := range n.Runs {
		r := &n.Runs[i]
		length := r.Length
		if length <= 0 {
			continue
		}
		if pos+length > len(units) {
			length = len(units) - pos
		}
		if length <= 0 {
			break
		}
		chunk := units[pos : pos+length]
		pos += length

		// A run can straddle newlines; split it so each line keeps its own runs.
		start := 0
		for j := 0; j <= len(chunk); j++ {
			if j == len(chunk) || chunk[j] == '\n' {
				// A run ending exactly on the newline belongs to the line it
				// terminates, not to the one that follows; otherwise paragraph
				// styles bleed downward.
				if j > start || j < len(chunk) {
					text := string(utf16.Decode(chunk[start:j]))
					if text != "" {
						cur.spans = append(cur.spans, spanFor(r, text))
					}
					cur.runs = append(cur.runs, r)
				}
				if j < len(chunk) {
					emit()
				}
				start = j + 1
			}
		}
	}
	if len(cur.spans) > 0 || len(cur.runs) > 0 {
		emit()
	}
	return out
}

func spanFor(r *AttributeRun, text string) span {
	return span{
		text:   text,
		bold:   r.FontWeight == 1 || r.FontHints&hintBold != 0,
		italic: r.FontHints&hintItalic != 0,
		strike: r.Strikethrough,
		mono:   strings.Contains(strings.ToLower(r.FontName), "mono") || strings.Contains(strings.ToLower(r.FontName), "courier"),
		link:   r.Link,
	}
}

func renderSpans(spans []span, wholeLineMono bool) string {
	var b strings.Builder
	for _, s := range spans {
		t := s.text
		if t == "" {
			continue
		}
		// Trailing whitespace inside emphasis markers breaks Markdown, so the
		// markers wrap only the trimmed core.
		lead := t[:len(t)-len(strings.TrimLeft(t, " \t"))]
		trail := t[len(strings.TrimRight(t, " \t")):]
		core := strings.TrimSpace(t)
		if core == "" {
			b.WriteString(t)
			continue
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
			core = "[" + core + "](" + s.link + ")"
		}
		b.WriteString(lead + core + trail)
	}
	return b.String()
}
