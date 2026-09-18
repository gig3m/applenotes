package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The machine running this is usually not the Mac holding the notes, so a
// command given -server must never fall through to a local database. It did:
// new, append, replace and rm were dispatched locally whatever -server said,
// and on Linux every one of them failed on a file that was never going to
// exist. This subjects the whole command table to the rule at once, so a
// command added later without a remote branch is caught here rather than by
// someone trying to use it.
func TestServerFlagNeverFallsBackToTheLocalDatabase(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1/notes":
			json.NewEncoder(w).Encode(map[string]any{"uuid": "NEW-UUID"})
		case r.Method == "DELETE":
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/v1/notes" || r.URL.Path == "/v1/folders" || r.URL.Path == "/v1/search":
			w.Write([]byte(`[]`)) // these return a list, not an object
		default:
			json.NewEncoder(w).Encode(map[string]any{
				"uuid": "U", "title": "t", "markdown": "body",
			})
		}
	}))
	defer srv.Close()

	// A path that cannot be opened: any attempt to use it shows up as an error
	// naming it, which is exactly the failure being tested for.
	const noDB = "/nonexistent/applenotes-test/NoteStore.sqlite"

	for _, tc := range []struct {
		name, stdin string
		args        []string
	}{
		{"list", "", []string{"list"}},
		{"folders", "", []string{"folders"}},
		{"show", "", []string{"show", "U"}},
		{"search", "", []string{"search", "hello"}},
		{"new", "body\n", []string{"new"}},
		{"append", "more\n", []string{"append", "U"}},
		{"replace", "fresh\n", []string{"replace", "U"}},
		{"rm", "", []string{"rm", "U"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got = nil
			// Flags first: for the free-text commands (capture, search) a flag
			// written after the text is part of the text, by design.
			args := []string{tc.args[0], "-server", srv.URL, "-token", "t", "-db", noDB}
			args = append(args, tc.args[1:]...)
			var out, errb bytes.Buffer
			code := run(args, strings.NewReader(tc.stdin), &out, &errb)

			if strings.Contains(errb.String(), noDB) {
				t.Fatalf("reached for the local database despite -server: %s", errb.String())
			}
			if len(got) == 0 {
				t.Fatalf("never called the server (exit %d): %s", code, errb.String())
			}
			if code != 0 {
				t.Errorf("exit %d: %s", code, errb.String())
			}
		})
	}
}

// The write commands must refuse an empty body remotely too, or a mistyped pipe
// blanks a note over the network just as easily as locally.
func TestRemoteWritesRefuseAnEmptyBody(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	for _, args := range [][]string{{"new"}, {"append", "U"}, {"replace", "U"}} {
		called = false
		full := append(append([]string{}, args...), "-server", srv.URL, "-token", "t")
		var out, errb bytes.Buffer
		if code := run(full, strings.NewReader("   \n\t\n"), &out, &errb); code == 0 {
			t.Errorf("%v: accepted an empty body", args)
		}
		if called {
			t.Errorf("%v: sent an empty body to the server", args)
		}
	}
}
