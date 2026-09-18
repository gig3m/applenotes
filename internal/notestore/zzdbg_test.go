package notestore

import (
	"fmt"
	"testing"
)

func rvShow(t *testing.T, label string, srcs []rvSrc) {
	n := rvBuild(srcs)
	md := n.Markdown()
	h := ToHTML(md)
	var rend []string
	for _, l := range rvParse(h) {
		rend = append(rend, rvDisplay(l))
	}
	fmt.Printf("%s\n  note=%q\n  md  =%q\n  html=%q\n  out =%q\n\n", label, n.Text, md, h, rend)
}

func TestRVMinimal(t *testing.T) {
	// A: inline code span with >=2 backticks -> 3-backtick delimiter -> eaten as a fence
	rvShow(t, "A1 mono span containing ``", []rvSrc{{text: "a``b", style: StyleBody, mono: true}, {text: "next", style: StyleBody}})
	rvShow(t, "A2 hand-written ```x``` ", nil)
	fmt.Printf("A2 ToHTML(\"```x```\\nrest\") = %q\n", ToHTML("```x```\nrest"))
	fmt.Printf("A3 ToHTML(\"```x```  \\nrest\") = %q\n", ToHTML("```x```  \nrest"))
	fmt.Printf("A4 ToHTML(\"``` go \\nrest\") = %q\n", ToHTML("``` go \nrest"))
	fmt.Printf("A5 ToHTML(\"```go\\nx\\n```\") = %q\n", ToHTML("```go\nx\n```"))

	// B: leading tab on a non-mono line
	rvShow(t, "B1 leading tab", []rvSrc{{text: "\tx", style: StyleBody}})
	rvShow(t, "B2 leading 4 spaces", []rvSrc{{text: "    x", style: StyleBody}})
	rvShow(t, "B3 leading tab+tab", []rvSrc{{text: "\t\tx", style: StyleBody}})

	// C: whitespace-only line
	rvShow(t, "C1 whitespace-only line", []rvSrc{{text: "a", style: StyleBody}, {text: "   ", style: StyleBody}, {text: "b", style: StyleBody}})
	fmt.Printf("C2 ToHTML(\"a\\n   \\nb\") = %q\n", ToHTML("a\n   \nb"))

	// D: entity text inside a mono span
	rvShow(t, "D1 mono span containing &amp;", []rvSrc{{text: "&amp;", style: StyleBody, mono: true}})
	rvShow(t, "D2 mono span containing &#160;", []rvSrc{{text: "x&#160;y", style: StyleBody, mono: true}})
	rvShow(t, "D3 plain span containing &amp;", []rvSrc{{text: "&amp;", style: StyleBody}})
	rvShow(t, "D4 mono paragraph containing &amp;", []rvSrc{{text: "&amp;", style: StyleMonospace}})

	// E: fence inside a list / unterminated fence
	fmt.Printf("E1 ToHTML(\"- a\\n```\\ncode\\n```\\n- b\") = %q\n", ToHTML("- a\n```\ncode\n```\n- b"))
	fmt.Printf("E2 ToHTML(\"```\\ncode\") = %q\n", ToHTML("```\ncode"))
	fmt.Printf("E3 ToHTML(\"```\\n```go\\nx\\n```\") = %q\n", ToHTML("```\n```go\nx\n```"))
	fmt.Printf("E4 ToHTML(\"    ```\\nx\\n    ```\") = %q\n", ToHTML("    ```\nx\n    ```"))

	// F: checklist separator
	fmt.Printf("F1 ToHTML(\"- [ ]   task\") = %q\n", ToHTML("- [ ]   task"))
	fmt.Printf("F2 ToHTML(\"- [x]\\ttask\") = %q\n", ToHTML("- [x]\ttask"))
	fmt.Printf("F3 ToHTML(\"- [ ]\\n\") = %q\n", ToHTML("- [ ]\n"))

	// G: code span padding strip
	fmt.Printf("G1 ToHTML(\"`  x  `\") = %q\n", ToHTML("`  x  `"))
	fmt.Printf("G2 ToHTML(\"a `b ` c\") = %q\n", ToHTML("a `b ` c"))
}
