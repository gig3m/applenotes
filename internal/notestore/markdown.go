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

// headingMinSize is the smallest point size Notes uses for a heading created
// through HTML; body text is 12pt.
const headingMinSize = 16

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
	var monoBuf []string
	inMono := false

	// The fence is sized to the content so a paragraph that itself contains
	// backtick fences cannot break out of its own block.
	closeMono := func() {
		if !inMono {
			return
		}
		fence := strings.Repeat("`", maxBacktickRun(monoBuf)+1)
		out = append(out, fence)
		out = append(out, monoBuf...)
		out = append(out, fence)
		monoBuf = nil
		inMono = false
	}

	for _, ln := range lines {
		style := ln.style()
		indent := ln.indent()
		body := renderSpans(ln.spans, style == StyleMonospace)
		blank := strings.Trim(body, " \t\r\n\v\f") == ""

		// Monospaced paragraphs become a fenced block, and their contents are
		// emitted verbatim -- escaping inside a fence would be visible.
		if style == StyleMonospace {
			inMono = true
			monoBuf = append(monoBuf, body)
			continue
		}
		closeMono()

		if blank {
			// A blank line does not end a numbered list in Notes, so the
			// counters are left alone.
			out = append(out, "")
			continue
		}

		if style != StyleNumList {
			// Ending a list at this level ends every level nested under it.
			delete(counters, indent)
			for d := range counters {
				if d > indent {
					delete(counters, d)
				}
			}
		}
		if style == StyleBody {
			// Notes has no paragraph style for a heading created through HTML:
			// it stores one as bold text at an enlarged point size. Recovering
			// it here is what lets a heading survive a write-then-read cycle.
			if h := ln.htmlHeading(); h != "" {
				// The bold is what encodes the heading, so emitting it inline
				// too would double-mark the line as "## **text**".
				out = append(out, h+renderSpans(unbold(ln.spans), false))
				continue
			}
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
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
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

// htmlHeading returns the Markdown prefix for a line Notes stored as enlarged
// bold text, or "" if it is ordinary body text. Every run on the line must
// agree, so a bolded phrase inside a paragraph is not mistaken for a heading.
func (l line) htmlHeading() string {
	size := float32(0)
	for _, r := range l.runs {
		if !r.Bold() || r.PointSize < headingMinSize {
			return ""
		}
		if r.PointSize > size {
			size = r.PointSize
		}
	}
	switch {
	case size >= 24:
		return "# "
	case size >= headingMinSize:
		return "## "
	}
	return ""
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
		if length < 0 {
			// The run table is corrupt. Stop here rather than skipping the run
			// without advancing pos, which would misalign everything after it;
			// the uncovered tail is then emitted unstyled below.
			break
		}
		if length == 0 {
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
		mono:       isMonoFont(name),
		link:       r.Link,
		attachment: r.Attachment,
	}
}

func renderSpans(spans []span, wholeLineMono bool) string {
	// Inside a fence nothing is markup: no escaping, no emphasis, no links.
	// Emitting them would put literal ** and [](...) into a code block.
	if wholeLineMono {
		var b strings.Builder
		for _, s := range spans {
			b.WriteString(s.text)
		}
		return b.String()
	}

	var b strings.Builder
	merged := mergeSpans(spans)
	for i := 0; i < len(merged); i++ {
		// Consecutive spans sharing a link are one link. They are often split
		// because something inside differs -- a code span, a bold word -- and
		// emitting each separately turns one link into several.
		if l := merged[i].link; l != "" {
			j := i
			for j+1 < len(merged) && merged[j+1].link == l {
				j++
			}
			group := make([]span, 0, j-i+1)
			for _, g := range merged[i : j+1] {
				g.link = ""
				group = append(group, g)
			}
			inner := renderSpans(group, wholeLineMono)
			b.WriteString("[" + inner + "](" + escapeURL(l) + ")")
			i = j
			continue
		}
		s := merged[i]
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
			// Also guards codeSpan, which has no sensible empty form.
			b.WriteString(t)
			continue
		}

		// A code span is literal, so its content must not be backslash-escaped;
		// the delimiter is instead sized to the content.
		if s.mono {
			core = codeSpan(core)
		} else {
			core = escapeText(core)
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
		b.WriteString(lead + core + trail)
	}
	return escapeLineStart(b.String())
}

// unbold clears the bold flag, for a line whose boldness is carrying its
// heading level rather than emphasis.
func unbold(spans []span) []span {
	out := make([]span, len(spans))
	copy(out, spans)
	for i := range out {
		out[i].bold = false
	}
	return out
}

// mergeSpans joins neighbouring spans that resolve to the same styling. Runs
// often differ only in attributes this renderer ignores (colour, underline),
// and leaving them split would let a character reference straddle a boundary
// and survive escaping.
func mergeSpans(spans []span) []span {
	if len(spans) < 2 {
		return spans
	}
	out := make([]span, 0, len(spans))
	for _, s := range spans {
		if n := len(out); n > 0 && s.attachment == nil && out[n-1].attachment == nil &&
			out[n-1].bold == s.bold && out[n-1].italic == s.italic &&
			out[n-1].strike == s.strike && out[n-1].mono == s.mono &&
			out[n-1].link == s.link {
			out[n-1].text += s.text
			continue
		}
		out = append(out, s)
	}
	return out
}

// codeSpan wraps text in backticks long enough to survive any backticks inside
// it, padding with spaces where CommonMark requires it.
func codeSpan(text string) string {
	fence := strings.Repeat("`", longestBacktickRun(text)+1)
	pad := ""
	if strings.HasPrefix(text, "`") || strings.HasSuffix(text, "`") {
		pad = " "
	}
	return fence + pad + text + pad + fence
}

func longestBacktickRun(s string) int {
	best, cur := 0, 0
	for _, r := range s {
		if r == '`' {
			cur++
			if cur > best {
				best = cur
			}
		} else {
			cur = 0
		}
	}
	return best
}

func maxBacktickRun(lines []string) int {
	best := 2
	for _, l := range lines {
		if n := longestBacktickRun(l); n > best {
			best = n
		}
	}
	return best
}

// isMonoFont reports whether a font name denotes a monospaced face. Menlo is
// what macOS actually ships for monospaced text.
func isMonoFont(name string) bool {
	for _, f := range []string{"mono", "courier", "menlo", "consolas"} {
		if strings.Contains(name, f) {
			return true
		}
	}
	return false
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
	return "[" + escapeText(label) + "](" + escapeURL("applenotes:attachment/"+s.attachment.Identifier) + ")"
}

// escapeText neutralises inline Markdown metacharacters so note text is never
// reinterpreted as markup.
func escapeText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i, r := range s {
		switch r {
		case '\\', '`', '*', '_', '[', ']', '<', '>', '~':
			b.WriteByte('\\')
		case '&':
			// Only an & that could begin a character reference needs escaping;
			// escaping every "Smith & Jones" would be noise.
			if looksLikeEntity(s[i:]) {
				b.WriteByte('\\')
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

// looksLikeEntity reports whether s begins with something a Markdown renderer
// would decode as a character reference.
func looksLikeEntity(s string) bool {
	rest := strings.TrimPrefix(s, "&")
	rest = strings.TrimPrefix(rest, "#")
	n := 0
	for n < len(rest) && n < 32 {
		c := rest[n]
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
			n++
			continue
		}
		break
	}
	return n > 0 && n < len(rest) && rest[n] == ';'
}

// escapeLineStart neutralises leading characters that would otherwise turn a
// plain paragraph into a heading, a list item or a quote.
func escapeLineStart(s string) string {
	trimmed := strings.TrimLeftFunc(s, unicode.IsSpace)
	lead := s[:len(s)-len(trimmed)]
	if trimmed == "" {
		return s
	}
	// Leading whitespace reaching column 4 reads as an indented code block.
	// Tabs advance to the next multiple of 4, so this counts columns rather
	// than testing for a prefix. List nesting is applied later as a prefix, so
	// only body text that genuinely begins with whitespace reaches here.
	col := 0
	for _, r := range s {
		if r == ' ' {
			col++
		} else if r == '\t' {
			col += 4 - col%4
		} else {
			break
		}
		if col >= 4 {
			// Replacing just the first character with its numeric reference
			// stops the line beginning with whitespace while keeping the rest
			// of the indentation intact.
			first, size := utf8.DecodeRuneInString(s)
			return fmt.Sprintf("&#%d;", first) + s[size:]
		}
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
	needsWrap := strings.ContainsAny(u, "()")
	var b strings.Builder
	for _, r := range u {
		switch {
		case r == '<':
			b.WriteString("%3C")
		case r == '>':
			b.WriteString("%3E")
		case r == '\\':
			// A backslash escapes the next character inside a destination, so
			// it would be eaten -- or, in the wrapped form, would escape the
			// closing bracket and break the link entirely.
			b.WriteString("%5C")
		case r == '&':
			b.WriteString("%26")
		case unicode.IsSpace(r) || unicode.IsControl(r):
			// Whitespace is illegal in a destination even inside <>, so it is
			// percent-encoded. RFC 3986 encodes UTF-8 octets, not code points:
			// encoding the rune would turn U+2028 into "%2028", which decodes
			// as a space followed by "28".
			for _, c := range []byte(string(r)) {
				fmt.Fprintf(&b, "%%%02X", c)
			}
		default:
			b.WriteRune(r)
		}
	}
	if needsWrap {
		return "<" + b.String() + ">"
	}
	return b.String()
}
