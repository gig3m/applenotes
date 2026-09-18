package notestore

import (
	"fmt"
	"html"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"unicode/utf16"
)

// ---------------------------------------------------------------------------
// HTML render simulation: what Notes.app would display for a ToHTML output.
// ---------------------------------------------------------------------------

type rvSeg struct {
	text string
	mono bool
	bold bool
	ital bool
	link string
}

type rvLine struct {
	kind string // div, h1, h2, li-ul, li-ol
	segs []rvSeg
}

func (l rvLine) raw() string {
	var b strings.Builder
	for _, s := range l.segs {
		b.WriteString(s.text)
	}
	return b.String()
}

// rvParse turns ToHTML output into display lines.
func rvParse(h string) []rvLine {
	var out []rvLine
	var cur *rvLine
	listKind := ""
	mono, bold, ital := 0, 0, 0
	link := ""

	flush := func() { cur = nil }
	start := func(kind string) {
		out = append(out, rvLine{kind: kind})
		cur = &out[len(out)-1]
	}
	text := func(s string) {
		if s == "" {
			return
		}
		if cur == nil {
			start("div") // stray text outside a block
		}
		cur.segs = append(cur.segs, rvSeg{text: htmlUnescape(s), mono: mono > 0, bold: bold > 0, ital: ital > 0, link: link})
	}

	for i := 0; i < len(h); {
		j := strings.IndexByte(h[i:], '<')
		if j < 0 {
			text(h[i:])
			break
		}
		text(h[i : i+j])
		i += j
		k := strings.IndexByte(h[i:], '>')
		if k < 0 {
			break
		}
		tag := h[i+1 : i+k]
		i += k + 1
		lower := strings.ToLower(tag)
		name := lower
		if sp := strings.IndexAny(name, " \t"); sp >= 0 {
			name = name[:sp]
		}
		switch name {
		case "div", "h1", "h2", "h3":
			start(name)
		case "li":
			kind := "li-ul"
			if listKind == "ol" {
				kind = "li-ol"
			}
			start(kind)
		case "/div", "/h1", "/h2", "/h3", "/li":
			flush()
		case "ul", "ol":
			listKind = name
		case "/ul", "/ol":
			listKind = ""
		case "br":
			if cur == nil {
				start("div")
			}
		case "font":
			if strings.Contains(lower, `face="menlo"`) {
				mono++
			} else {
				mono++ // treat any font tag as a style boundary
			}
		case "/font":
			if mono > 0 {
				mono--
			}
		case "b":
			bold++
		case "/b":
			if bold > 0 {
				bold--
			}
		case "i":
			ital++
		case "/i":
			if ital > 0 {
				ital--
			}
		case "a":
			if p := strings.Index(lower, `href="`); p >= 0 {
				rest := tag[p+6:]
				if q := strings.IndexByte(rest, '"'); q >= 0 {
					link = htmlUnescape(rest[:q])
				}
			}
		case "/a":
			link = ""
		}
	}
	return out
}

func htmlUnescape(s string) string { return html.UnescapeString(s) }

// rvCollapse applies HTML whitespace collapsing: runs of collapsible space
// become one space, and collapsible space at either edge of the line box is
// dropped. U+00A0 is not collapsible.
func rvCollapse(s string) string {
	var b strings.Builder
	prev := false
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '\r', '\f':
			if !prev {
				b.WriteByte(' ')
				prev = true
			}
		default:
			prev = false
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), " ")
}

func rvNBSP(s string) string { return strings.ReplaceAll(s, " ", " ") }

// rvDisplay is the visible text of a rendered line.
func rvDisplay(l rvLine) string { return rvNBSP(rvCollapse(l.raw())) }

// rvExpect normalises a source line to the same domain: a tab is four columns
// by this tool's own convention, and a hard space reads as a space.
func rvExpect(s string) string {
	return rvNBSP(strings.ReplaceAll(s, "\t", "    "))
}

// ---------------------------------------------------------------------------
// note construction
// ---------------------------------------------------------------------------

type rvSrc struct {
	text   string
	style  int
	indent int
	bold   bool
	ital   bool
	strike bool
	size   float32
	mono   bool
	link   string
	quote  bool
	done   bool
}

func rvBuild(srcs []rvSrc) *Note {
	n := &Note{}
	var texts []string
	for _, s := range srcs {
		texts = append(texts, s.text)
	}
	n.Text = strings.Join(texts, "\n")
	for i, s := range srcs {
		t := s.text
		if i < len(srcs)-1 {
			t += "\n"
		}
		if t == "" {
			continue
		}
		ps := &ParagraphStyle{StyleType: s.style, IndentAmount: s.indent}
		if s.quote {
			ps.BlockQuote = 1
		}
		if s.style == StyleChecklist {
			ps.Checklist = &Checklist{Done: s.done}
		}
		w := FontDefault
		switch {
		case s.bold && s.ital:
			w = FontBoldItalic
		case s.bold:
			w = FontBold
		case s.ital:
			w = FontItalic
		}
		fn := ""
		if s.mono {
			fn = "Menlo"
		}
		size := s.size
		if size == 0 {
			size = 12
		}
		n.Runs = append(n.Runs, AttributeRun{
			Length:         len(utf16.Encode([]rune(t))),
			ParagraphStyle: ps,
			FontName:       fn,
			PointSize:      size,
			FontWeight:     w,
			Strikethrough:  s.strike,
			Link:           s.link,
		})
	}
	return n
}

// ---------------------------------------------------------------------------
// generator
// ---------------------------------------------------------------------------

var rvPieces = []string{
	"a", "Bc", "7", "word", " ", "  ", "   ", "\t", "\t\t", "é", "中", "🙂", " ",
	"-", "*", "_", "`", "``", "```", "````", "~~~", "~", "#", "##", "###", ">", ">>",
	"[", "]", "(", ")", "\\", "&", "&#160;", "&amp;", ".", "1.", "2)", "[ ]", "[x]",
	"- ", "+ ", "=", "<", ">", "\"", "'", "!", "|", "{", "}", "http://x.y/a", "$",
	"**b**", "*i*", "[t](u)", "```go", "  x  ", "a\tb", " ` ", "x  y",
}

func rvRandLine(r *rand.Rand) string {
	n := r.Intn(7)
	var b strings.Builder
	if r.Intn(4) == 0 {
		b.WriteString(strings.Repeat(" ", r.Intn(6)))
	}
	if r.Intn(8) == 0 {
		b.WriteString(strings.Repeat("\t", 1+r.Intn(2)))
	}
	for i := 0; i < n; i++ {
		b.WriteString(rvPieces[r.Intn(len(rvPieces))])
	}
	if r.Intn(4) == 0 {
		b.WriteString(strings.Repeat(" ", 1+r.Intn(4)))
	}
	return b.String()
}

var rvStyles = []int{StyleBody, StyleTitle, StyleHeading, StyleSubhead, StyleMonospace,
	StyleDotList, StyleDashList, StyleNumList, StyleChecklist}

func rvRandNote(r *rand.Rand, forceStyle int, indented bool) []rvSrc {
	n := 1 + r.Intn(8)
	out := make([]rvSrc, 0, n)
	for i := 0; i < n; i++ {
		s := rvSrc{text: rvRandLine(r), size: 12}
		if forceStyle != -999 {
			s.style = forceStyle
		} else {
			s.style = rvStyles[r.Intn(len(rvStyles))]
		}
		if indented {
			s.indent = r.Intn(3)
		}
		if r.Intn(6) == 0 {
			s.bold = true
		}
		if r.Intn(8) == 0 {
			s.ital = true
		}
		if r.Intn(10) == 0 {
			s.strike = true
		}
		if r.Intn(12) == 0 {
			s.mono = true
		}
		if r.Intn(10) == 0 && s.style == StyleBody && s.bold {
			s.size = []float32{18, 24}[r.Intn(2)]
		}
		if r.Intn(12) == 0 {
			s.quote = true
		}
		s.done = r.Intn(2) == 0
		out = append(out, s)
	}
	return out
}

// ---------------------------------------------------------------------------
// comparison
// ---------------------------------------------------------------------------

func rvNonSpace(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r != ' ' && r != '\t' && r != ' ' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func rvIsSub(a, b string) bool {
	ar, br := []rune(a), []rune(b)
	i := 0
	for j := 0; j < len(br) && i < len(ar); j++ {
		if ar[i] == br[j] {
			i++
		}
	}
	return i == len(ar)
}

// rvLost returns the runes of want that do not appear (in order) in got.
func rvLost(want, got string) []rune {
	w, g := []rune(want), []rune(got)
	// LCS over short lines.
	if len(w)*len(g) > 400000 {
		return nil
	}
	dp := make([][]int, len(w)+1)
	for i := range dp {
		dp[i] = make([]int, len(g)+1)
	}
	for i := len(w) - 1; i >= 0; i-- {
		for j := len(g) - 1; j >= 0; j-- {
			if w[i] == g[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var lost []rune
	i, j := 0, 0
	for i < len(w) {
		if j < len(g) && w[i] == g[j] {
			i++
			j++
			continue
		}
		if j < len(g) && dp[i+1][j] >= dp[i][j+1] {
			lost = append(lost, w[i])
			i++
			continue
		}
		if j < len(g) {
			j++
			continue
		}
		lost = append(lost, w[i])
		i++
	}
	return lost
}

func rvLead(s string) int {
	n := 0
	for _, r := range s {
		if r == ' ' {
			n++
			continue
		}
		break
	}
	return n
}

type rvReport struct {
	counts map[string]int
	sample map[string]string
}

func newReport() *rvReport {
	return &rvReport{counts: map[string]int{}, sample: map[string]string{}}
}

func (r *rvReport) add(class, ex string) {
	r.counts[class]++
	if _, ok := r.sample[class]; !ok {
		r.sample[class] = ex
	}
}

func (r *rvReport) dump(t *testing.T, title string) {
	t.Logf("=== %s ===", title)
	keys := make([]string, 0, len(r.counts))
	for k := range r.counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		t.Logf("  clean")
	}
	for _, k := range keys {
		t.Logf("  %-34s %6d   e.g. %s", k, r.counts[k], r.sample[k])
	}
}

// rvDiff returns the indices of want's runes that do not survive into got.
func rvDiff(want, got []rune) []int {
	n, m := len(want), len(got)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if want[i] == got[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var lost []int
	i, j := 0, 0
	for i < n {
		if j < m && want[i] == got[j] {
			i++
			j++
			continue
		}
		if j < m && dp[i+1][j] >= dp[i][j+1] {
			lost = append(lost, i)
			i++
			continue
		}
		if j < m {
			j++
			continue
		}
		lost = append(lost, i)
		i++
	}
	return lost
}

// rvClassify names the defect class for a lost rune at index i of want.
func rvClassify(want []rune, i int) string {
	c := want[i]
	if c == '\n' {
		return "line break lost"
	}
	if c != ' ' {
		return "NON-WHITESPACE LOST"
	}
	// find the enclosing line
	ls := i
	for ls > 0 && want[ls-1] != '\n' {
		ls--
	}
	le := i
	for le < len(want) && want[le] != '\n' {
		le++
	}
	allSpace := true
	for k := ls; k < le; k++ {
		if want[k] != ' ' {
			allSpace = false
			break
		}
	}
	if allSpace {
		return "blank-line whitespace lost"
	}
	leadEnd := ls
	for leadEnd < le && want[leadEnd] == ' ' {
		leadEnd++
	}
	trailStart := le
	for trailStart > ls && want[trailStart-1] == ' ' {
		trailStart--
	}
	switch {
	case i < leadEnd:
		return "leading whitespace lost"
	case i >= trailStart:
		return "trailing whitespace lost"
	}
	return "interior whitespace lost"
}

// rvCheck runs one note through the pipeline and records defects.
func rvCheck(rep *rvReport, srcs []rvSrc, tag string) {
	n := rvBuild(srcs)
	md := n.Markdown()
	h := ToHTML(md)
	lines := rvParse(h)

	// Trailing blank source lines are dropped by design at both stages.
	last := len(srcs) - 1
	for last >= 0 && strings.TrimSpace(srcs[last].text) == "" {
		last--
	}
	if last < 0 {
		return
	}
	var wl []string
	for _, s := range srcs[:last+1] {
		wl = append(wl, rvExpect(s.text))
	}
	var gl []string
	for _, l := range lines {
		gl = append(gl, rvDisplay(l))
	}
	want := []rune(strings.Join(wl, "\n"))
	got := []rune(strings.Join(gl, "\n"))
	if len(want)*len(got) > 4000000 {
		return
	}
	for _, i := range rvDiff(want, got) {
		cls := rvClassify(want, i)
		rep.add(cls, fmt.Sprintf("%s at %d\n      want=%q\n      md  =%q\n      html=%q\n      got =%q", tag, i, string(want), md, h, string(got)))
	}
}

func TestRVFuzzAllStyles(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	rep := newReport()
	for i := 0; i < 4000; i++ {
		rvCheck(rep, rvRandNote(r, -999, false), "mixed")
	}
	rep.dump(t, "mixed styles, no indent (4000 notes)")
}

func TestRVFuzzIndented(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	rep := newReport()
	for i := 0; i < 4000; i++ {
		rvCheck(rep, rvRandNote(r, -999, true), "indent")
	}
	rep.dump(t, "mixed styles, indented (4000 notes)")
}

func TestRVFuzzMono(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	rep := newReport()
	for i := 0; i < 4000; i++ {
		rvCheck(rep, rvRandNote(r, StyleMonospace, false), "mono")
	}
	rep.dump(t, "all monospace (4000 notes)")
}

func TestRVFuzzBody(t *testing.T) {
	r := rand.New(rand.NewSource(4))
	rep := newReport()
	for i := 0; i < 4000; i++ {
		rvCheck(rep, rvRandNote(r, StyleBody, false), "body")
	}
	rep.dump(t, "all body (4000 notes)")
}

func TestRVFuzzPerStyle(t *testing.T) {
	for _, st := range rvStyles {
		r := rand.New(rand.NewSource(int64(100 + st)))
		rep := newReport()
		for i := 0; i < 1500; i++ {
			rvCheck(rep, rvRandNote(r, st, false), fmt.Sprintf("s%d", st))
		}
		rep.dump(t, fmt.Sprintf("style %d (1500 notes)", st))
	}
}

// ---------------------------------------------------------------------------
// convergence: text -> Markdown -> ToHTML -> text -> ...
// ---------------------------------------------------------------------------

// rvNoteFromHTML models what Notes stores when given ToHTML's output.
func rvNoteFromHTML(h string) *Note {
	lines := rvParse(h)
	n := &Note{}
	var texts []string
	type runInfo struct {
		text string
		seg  rvSeg
		st   int
		size float32
	}
	var infos []runInfo
	for _, l := range lines {
		st := StyleBody
		size := float32(12)
		bold := false
		switch l.kind {
		case "h1":
			size, bold = 24, true
		case "h2":
			size, bold = 18, true
		case "li-ul":
			st = StyleDotList
		case "li-ol":
			st = StyleNumList
		}
		// collapse whitespace the way a renderer would, then store the text
		raw := rvCollapse(l.raw())
		texts = append(texts, raw)
		if len(l.segs) == 0 {
			infos = append(infos, runInfo{text: "", st: st, size: size, seg: rvSeg{bold: bold}})
			continue
		}
		// One run for the whole line; styling taken from the first segment.
		s := l.segs[0]
		s.bold = s.bold || bold
		infos = append(infos, runInfo{text: raw, seg: s, st: st, size: size})
	}
	n.Text = strings.Join(texts, "\n")
	for i, in := range infos {
		t := in.text
		if i < len(infos)-1 {
			t += "\n"
		}
		if t == "" {
			continue
		}
		w := FontDefault
		switch {
		case in.seg.bold && in.seg.ital:
			w = FontBoldItalic
		case in.seg.bold:
			w = FontBold
		case in.seg.ital:
			w = FontItalic
		}
		fn := ""
		if in.seg.mono {
			fn = "Menlo"
		}
		n.Runs = append(n.Runs, AttributeRun{
			Length:         len(utf16.Encode([]rune(t))),
			ParagraphStyle: &ParagraphStyle{StyleType: in.st},
			FontName:       fn,
			PointSize:      in.size,
			FontWeight:     w,
			Link:           in.seg.link,
		})
	}
	return n
}

func TestRVConvergence(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	worstTrips, worstGrowth := 0, 0
	var worstCase, worstGrowCase string
	nonConverged := 0
	for i := 0; i < 3000; i++ {
		srcs := rvRandNote(r, -999, r.Intn(2) == 0)
		n := rvBuild(srcs)
		start := len(n.Text)
		seen := map[string]bool{}
		trips := 0
		prev := ""
		for {
			md := n.Markdown()
			h := ToHTML(md)
			n = rvNoteFromHTML(h)
			trips++
			if n.Text == prev {
				trips-- // the trip that changed nothing
				break
			}
			if seen[n.Text] {
				nonConverged++
				t.Logf("CYCLE after %d trips: %q", trips, n.Text)
				break
			}
			seen[n.Text] = true
			prev = n.Text
			if trips > 20 {
				nonConverged++
				t.Logf("NO FIXED POINT: %q", n.Text)
				break
			}
		}
		if trips > worstTrips {
			worstTrips = trips
			worstCase = fmt.Sprintf("%q", rvBuild(srcs).Text)
		}
		if g := len(n.Text) - start; g > worstGrowth {
			worstGrowth = g
			worstGrowCase = fmt.Sprintf("%q -> %q", rvBuild(srcs).Text, n.Text)
		}
	}
	t.Logf("convergence: worst trips=%d, worst growth=%d bytes, non-converged=%d", worstTrips, worstGrowth, nonConverged)
	t.Logf("worst trips case: %s", worstCase)
	t.Logf("worst growth case: %s", worstGrowCase)
}
