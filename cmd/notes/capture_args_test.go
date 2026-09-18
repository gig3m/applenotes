package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// capture takes free text, so its arguments must reach the note exactly as
// typed. Rearranging them to rescue a misplaced flag looked harmless and was
// not: a word beginning with "-" was hoisted out of the sentence, and for a
// value-taking flag the following word went with it. "fix the -server timeout"
// became the note "fix the" sent to a daemon named "timeout" -- an ordinary
// sentence silently changing where the command writes.
func TestCaptureTextIsNeverRearranged(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"the", "-deleted", "flag", "is", "nice"}, "the -deleted flag is nice"},
		{[]string{"running", "-force", "tests"}, "running -force tests"},
		{[]string{"call", "Bob", "-token", "about", "lunch"}, "call Bob -token about lunch"},
		{[]string{"fix", "the", "-server", "timeout"}, "fix the -server timeout"},
		{[]string{"pay", "-db", "later"}, "pay -db later"},
		{[]string{"budget", "is", "-5", "dollars"}, "budget is -5 dollars"},
		{[]string{"what", "-h", "means"}, "what -h means"},
		{[]string{"hello", "--", "world"}, "hello -- world"},
		{[]string{"Kyle", "--", "call", "the", "bank"}, "Kyle -- call the bank"},
	} {
		t.Run(strings.Join(tc.args, "_"), func(t *testing.T) {
			var body string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var in struct{ Markdown string }
				json.NewDecoder(r.Body).Decode(&in)
				body = in.Markdown
				json.NewEncoder(w).Encode(map[string]any{"uuid": "U"})
			}))
			defer srv.Close()

			// Flags first, then the text: everything after the first
			// positional is the note, verbatim.
			args := append([]string{"capture", "-server", srv.URL, "-token", "t"}, tc.args...)
			var out, errb bytes.Buffer
			code := run(args, strings.NewReader(""), &out, &errb)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, errb.String())
			}
			if strings.TrimSpace(body) != tc.want {
				t.Errorf("note body = %q, want %q", body, tc.want)
			}
		})
	}
}

// The commands whose positionals are UUIDs still accept a flag written after
// them, which is what permute is for.
func TestUUIDCommandsStillAcceptATrailingFlag(t *testing.T) {
	var hit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = r.Method + " " + r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	var out, errb bytes.Buffer
	code := run([]string{"rm", "UUID-A", "-server", srv.URL, "-token", "t"},
		strings.NewReader(""), &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if hit != "DELETE /v1/notes/UUID-A" {
		t.Errorf("hit %q, want the delete route", hit)
	}
}

// A value-taking flag with nothing after it is a mistake the user should hear
// about. permute must not manufacture a value for it out of its own separator.
func TestTrailingFlagWithNoValueIsAnError(t *testing.T) {
	for _, args := range [][]string{
		{"rm", "UUID", "-server"},
		{"show", "UUID", "-folder"},
	} {
		var out, errb bytes.Buffer
		code := run(args, strings.NewReader(""), &out, &errb)
		if code != 2 {
			t.Errorf("%v: exit %d, want 2 (a usage error)", args, code)
		}
		// Both the old behaviours exited non-zero too, so the exit code alone
		// proves nothing: it has to say the flag is incomplete, rather than
		// reporting a daemon named "--" or no daemon configured at all.
		if !strings.Contains(errb.String(), "needs an argument") {
			t.Errorf("%v: did not report the incomplete flag: %s", args, errb.String())
		}
	}
}

// A flag written after the query is part of the query, which is correct but
// looks exactly like the folder being empty. The user gets told rather than
// left to work it out from zero results.
func TestAFlagInsideASearchQueryIsPointedOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	var out, errb bytes.Buffer
	code := run([]string{"search", "-server", srv.URL, "-token", "t", "meeting", "-folder", "Work"},
		strings.NewReader(""), &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	for _, want := range []string{"-folder", "part of the text", "Write flags first"} {
		if !strings.Contains(errb.String(), want) {
			t.Errorf("stderr does not mention %q: %s", want, errb.String())
		}
	}
}

// Ordinary words must not trip the warning, or it becomes noise and stops
// being read.
func TestAnOrdinarySearchSaysNothingExtra(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	for _, q := range [][]string{{"meeting", "notes"}, {"budget", "-5", "dollars"}, {"a-b", "c"}} {
		var out, errb bytes.Buffer
		args := append([]string{"search", "-server", srv.URL, "-token", "t"}, q...)
		if code := run(args, strings.NewReader(""), &out, &errb); code != 0 {
			t.Fatalf("%v: exit %d: %s", q, code, errb.String())
		}
		if errb.Len() != 0 {
			t.Errorf("%v: unexpected warning: %s", q, errb.String())
		}
	}
}
