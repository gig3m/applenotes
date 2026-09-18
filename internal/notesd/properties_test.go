package notesd

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// These tests were written before the daemon, to pin the properties that matter
// rather than the shape of the code. They describe what must be true of every
// route, so a route added later without thinking is caught by the table.

// routes lists every endpoint with a request body that should otherwise be
// valid. Adding a route means adding a row, and every property below then
// applies to it automatically.
func routes() []struct {
	method, path, body string
	mutates            bool
} {
	return []struct {
		method, path, body string
		mutates            bool
	}{
		{"GET", "/v1/healthz", "", false},
		{"GET", "/v1/folders", "", false},
		{"GET", "/v1/notes", "", false},
		{"GET", "/v1/notes/UUID-PLAIN", "", false},
		{"GET", "/v1/search?q=text", "", false},
		{"POST", "/v1/notes", `{"markdown":"hello"}`, true},
		{"PUT", "/v1/notes/UUID-PLAIN", `{"markdown":"hello"}`, true},
		{"POST", "/v1/notes/UUID-PLAIN/append", `{"markdown":"more"}`, true},
		{"DELETE", "/v1/notes/UUID-PLAIN", "", true},
		// MCP is subject to every property above: auth, JSON-ness, no 5xx on
		// malformed input, never writing the database.
		{"POST", "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, false},
	}
}

// Property: no route is reachable without the token, and healthz is not an
// exception. A daemon on a shared tailnet has no other perimeter.
func TestEveryRouteRequiresAuth(t *testing.T) {
	srv := newTestServer(t)
	for _, r := range routes() {
		for _, auth := range []string{"", "Bearer wrong", "Basic " + testToken} {
			rec := srv.do(t, r.method, r.path, r.body, auth)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s with %q: got %d, want 401", r.method, r.path, auth, rec.Code)
			}
		}
	}
}

// Property: only the exact token in an exact Bearer header is accepted. Each
// case isolates one way the check could be loosened -- a scheme that is not
// Bearer, a prefix of the token, an extension of it, or the token in the wrong
// place -- because a single wrong-token case fails for several reasons at once
// and so pins none of them.
func TestAuthAcceptsOnlyTheExactBearerToken(t *testing.T) {
	srv := newTestServer(t)
	for _, auth := range []string{
		"",
		testToken,                                // no scheme
		"Basic " + testToken,                     // wrong scheme
		"bearer " + testToken,                    // scheme case: RFC says insensitive, we require exact
		"Bearer " + testToken[:len(testToken)-1], // a prefix
		"Bearer " + testToken + "x",              // an extension
		"Bearer  " + testToken,                   // extra space
		"Bearer " + strings.ToUpper(testToken),
	} {
		if rec := srv.do(t, "GET", "/v1/notes", "", auth); rec.Code != http.StatusUnauthorized {
			t.Errorf("%q was accepted (%d)", auth, rec.Code)
		}
	}
	if rec := srv.do(t, "GET", "/v1/notes", "", "Bearer "+testToken); rec.Code != http.StatusOK {
		t.Errorf("the correct token was rejected (%d)", rec.Code)
	}
}

// Property: a write with no content is refused. An earlier version of the
// malformed-input property accepted any status below 500, so a 202 for an empty
// body passed it.
func TestEmptyMarkdownIsRefused(t *testing.T) {
	fakeOsascript(t)
	srv := newTestServer(t)
	for _, r := range routes() {
		if !r.mutates || r.method == "DELETE" {
			continue
		}
		for _, body := range []string{`{"markdown":""}`, `{"markdown":"   \n"}`, `{}`} {
			rec := srv.do(t, r.method, r.path, body, "Bearer "+testToken)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s %s with %s: got %d, want 400", r.method, r.path, body, rec.Code)
			}
		}
	}
}

// Property: a wrong token must not be distinguishable from a missing one by
// anything the response says.
func TestAuthFailuresAreIndistinguishable(t *testing.T) {
	srv := newTestServer(t)
	var bodies []string
	for _, auth := range []string{"", "Bearer wrong", "Bearer " + testToken + "x"} {
		bodies = append(bodies, srv.do(t, "GET", "/v1/notes", "", auth).Body.String())
	}
	for i := 1; i < len(bodies); i++ {
		if bodies[i] != bodies[0] {
			t.Errorf("auth failures differ: %q vs %q", bodies[0], bodies[i])
		}
	}
}

// Property: malformed input never reaches Notes as something the daemon failed
// to anticipate. Either it is rejected with a 4xx, or it is handled exactly as
// a valid request would be -- osascript does not exist here, so a valid write
// fails at the Apple Event, and matching that status means the daemon treated
// the input uniformly rather than stumbling on it.
func TestMalformedInputIsNeverServerError(t *testing.T) {
	srv := newTestServer(t)
	bodies := []string{
		"", "{", "null", "[]", `{"markdown":null}`, `{"markdown":123}`,
		`{"markdown":""}`, `{"markdown":"   "}`, `{"unknown":"x"}`,
		`{"markdown":"` + strings.Repeat("x", 1<<20) + `"}`,
		"{\"markdown\":\"a\\u0000b\"}",
	}
	for _, r := range routes() {
		// DELETE carries no body, so nothing here is input it could reject.
		if !r.mutates || r.method == "DELETE" {
			continue
		}
		valid := srv.do(t, r.method, r.path, r.body, "Bearer "+testToken).Code
		for _, b := range bodies {
			rec := srv.do(t, r.method, r.path, b, "Bearer "+testToken)
			if rec.Code < 500 || rec.Code == valid {
				continue
			}
			t.Errorf("%s %s with %.40q: got %d, want 4xx or the valid-request status %d",
				r.method, r.path, b, rec.Code, valid)
		}
	}
}

// Property: a write that would destroy content is refused, and the refusal says
// what would have been lost. This is the guarantee the whole write path exists
// to provide; it must not be lost at the HTTP layer.
func TestWritesRefuseToDestroyContent(t *testing.T) {
	srv := newTestServer(t)
	for _, r := range routes() {
		if !r.mutates || !strings.Contains(r.path, "UUID-PLAIN") {
			continue
		}
		path := strings.Replace(r.path, "UUID-PLAIN", "UUID-ATTACH", 1)
		rec := srv.do(t, r.method, path, r.body, "Bearer "+testToken)
		if r.method == "DELETE" {
			continue // deleting is recoverable; Notes keeps it for 30 days
		}
		if rec.Code != http.StatusConflict {
			t.Errorf("%s %s: got %d, want 409", r.method, path, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), "attachments") {
			t.Errorf("%s %s: refusal does not say what would be lost: %s", r.method, path, rec.Body)
		}
	}
}

// Property: force overrides the refusal, and nothing else does.
func TestForceOverridesOnlyWhenAsked(t *testing.T) {
	srv := newTestServer(t)
	rec := srv.do(t, "PUT", "/v1/notes/UUID-ATTACH", `{"markdown":"x","force":true}`, "Bearer "+testToken)
	if rec.Code == http.StatusConflict {
		t.Errorf("force did not override the guard: %d %s", rec.Code, rec.Body)
	}
}

// Property: the daemon never writes to the notes database. Writes go through
// Notes.app, which owns that file.
func TestDatabaseIsNeverWritten(t *testing.T) {
	srv := newTestServer(t)
	before := srv.dbDigest(t)
	for _, r := range routes() {
		srv.do(t, r.method, r.path, r.body, "Bearer "+testToken)
	}
	if after := srv.dbDigest(t); after != before {
		t.Error("the database changed; the daemon must only read it")
	}
}

// Property: concurrent requests do not race. OnDegrade in particular is a field
// on the Writer, so a shared Writer would report one request's degradation to
// another. Run with -race.
func TestConcurrentRequestsDoNotRace(t *testing.T) {
	srv := newTestServer(t)
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := routes()[i%len(routes())]
			srv.do(t, r.method, r.path, r.body, "Bearer "+testToken)
		}(i)
	}
	wg.Wait()
}

// Property: a write is reported as accepted, not as done. Notes.app persists on
// its own schedule, so a 200 would be a lie about durability.
//
// This needs a working osascript, which the test machine does not have -- an
// earlier version of this test allowed any 5xx and so never asserted anything.
// fakeOsascript puts a stub on PATH so the write actually succeeds.
func TestWritesReportAcceptedNotCompleted(t *testing.T) {
	fakeOsascript(t)
	srv := newTestServer(t)
	for _, r := range routes() {
		if !r.mutates {
			continue
		}
		rec := srv.do(t, r.method, r.path, r.body, "Bearer "+testToken)
		if rec.Code != http.StatusAccepted {
			t.Errorf("%s %s: got %d, want 202 Accepted (body %s)", r.method, r.path, rec.Code, rec.Body)
		}
	}
}

// Property: a body over the limit is refused rather than read into memory. The
// earlier fixture stopped well short of the cap, so deleting it changed nothing.
func TestOversizedBodyIsRefused(t *testing.T) {
	srv := newTestServer(t)
	big := `{"markdown":"` + strings.Repeat("x", maxBody+1024) + `"}`
	rec := srv.do(t, "PUT", "/v1/notes/UUID-PLAIN", big, "Bearer "+testToken)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400 from the body cap", rec.Code)
	}
	// Specifically the cap, not the later argv limit: without MaxBytesReader
	// the body decodes fine and is only refused further down, which is a 4xx
	// too and would let the cap be deleted unnoticed.
	if !strings.Contains(rec.Body.String(), "too large") {
		t.Errorf("refused for the wrong reason: %s", rec.Body)
	}
}

// Property: advice is only offered where it can be taken. append has no force,
// so telling a caller to retry with force would send them in a circle.
func TestRefusalAdviceMatchesTheOperation(t *testing.T) {
	srv := newTestServer(t)
	for _, tc := range []struct {
		method, path string
		wantForce    bool
	}{
		{"PUT", "/v1/notes/UUID-ATTACH", true},
		{"POST", "/v1/notes/UUID-ATTACH/append", false},
	} {
		rec := srv.do(t, tc.method, tc.path, `{"markdown":"x"}`, "Bearer "+testToken)
		if rec.Code != http.StatusConflict {
			t.Fatalf("%s %s: got %d, want 409", tc.method, tc.path, rec.Code)
		}
		mentionsForce := strings.Contains(rec.Body.String(), "retry with force")
		if mentionsForce != tc.wantForce {
			t.Errorf("%s %s: force advice = %v, want %v: %s",
				tc.method, tc.path, mentionsForce, tc.wantForce, rec.Body)
		}
	}
}

// Property: a missing note is 404 and says so, distinguishably from a missing
// route. The mux's own 404 was swallowing handler-generated ones.
func TestMissingNoteIsNotFoundAndSaysWhy(t *testing.T) {
	srv := newTestServer(t)
	for _, r := range []struct{ method, path, body string }{
		{"GET", "/v1/notes/UUID-NOSUCH", ""},
		{"PUT", "/v1/notes/UUID-NOSUCH", `{"markdown":"x"}`},
	} {
		rec := srv.do(t, r.method, r.path, r.body, "Bearer "+testToken)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: got %d, want 404", r.method, r.path, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), "no such note") {
			t.Errorf("%s %s: cannot tell a missing note from a missing route: %s",
				r.method, r.path, rec.Body)
		}
	}
}

// fakeOsascript puts a stub osascript first on PATH for the duration of a test,
// so the write path can be exercised off a Mac.
func fakeOsascript(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n# swallow the script on stdin, then answer as Notes would\ncat >/dev/null\necho 'x-coredata://STORE-UUID/ICNote/p10'\n"
	if err := os.WriteFile(filepath.Join(dir, "osascript"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// Property: reading a note reports what a rewrite would flatten, so a client can
// warn before it round-trips the note through Markdown.
func TestReadReportsWhatARewriteWouldFlatten(t *testing.T) {
	srv := newTestServer(t)
	rec := srv.do(t, "GET", "/v1/notes/UUID-DEGRADED", "", "Bearer "+testToken)
	var got struct {
		Degrades []string `json:"degrades"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	if len(got.Degrades) == 0 {
		t.Error("an indented, underlined note reported nothing")
	}
}

// Property: every response is JSON, including every error. A client should not
// have to parse prose to find out what happened.
func TestEveryResponseIsJSON(t *testing.T) {
	srv := newTestServer(t)
	for _, r := range routes() {
		for _, auth := range []string{"", "Bearer " + testToken} {
			rec := srv.do(t, r.method, r.path, r.body, auth)
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("%s %s (auth %q): Content-Type %q", r.method, r.path, auth, ct)
			}
			if rec.Body.Len() > 0 && !json.Valid(rec.Body.Bytes()) {
				t.Errorf("%s %s: body is not JSON: %s", r.method, r.path, rec.Body)
			}
		}
	}
}

// Property: an unknown route is a clean 404, not a panic or a redirect.
func TestUnknownRoutesAreNotFound(t *testing.T) {
	srv := newTestServer(t)
	for _, path := range []string{"/", "/v1", "/v1/notes/../../etc/passwd", "/v1/notes//append", "/v2/notes"} {
		rec := srv.do(t, "GET", path, "", "Bearer "+testToken)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET %s: got %d, want 404 or 405", path, rec.Code)
		}
	}
}

// --- harness -----------------------------------------------------------------

const testToken = "test-token-0123456789"

type testServer struct {
	h      http.Handler
	dbPath string
}

func (s *testServer) do(t *testing.T, method, path, body, auth string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, req)
	return rec
}

func (s *testServer) dbDigest(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(s.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

// Property: a panicking handler is one failed request, not a dropped
// connection. A daemon going quiet looks exactly like the Mac being asleep.
func TestPanicBecomesAnError(t *testing.T) {
	srv := newTestServer(t)
	panicking := recoverPanics(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("note text that must not be echoed: SECRETNOTE")
	}))
	rec := httptest.NewRecorder()
	panicking.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/notes", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("got %d, want 500", rec.Code)
	}
	if !json.Valid(rec.Body.Bytes()) {
		t.Errorf("body is not JSON: %q", rec.Body)
	}
	// A panic value can carry anything that was in scope.
	if strings.Contains(rec.Body.String(), "SECRETNOTE") {
		t.Errorf("the panic value reached the response: %q", rec.Body)
	}
	_ = srv
}
