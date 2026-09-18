package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Written before the client. The properties are about how it behaves toward a
// server it does not control, which is the part that matters on a tailnet: the
// Mac can be asleep, mid-reboot, or answering with something unexpected.

// Property: the token goes on every request and never anywhere else.
func TestTokenIsSentOnEveryRequestAndNowhereElse(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		if q := r.URL.RawQuery; strings.Contains(q, testToken) {
			t.Errorf("token leaked into the query string: %s", q)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := New(srv.URL, testToken)
	ctx := context.Background()
	_, _ = c.Notes(ctx, ListOptions{})
	_, _ = c.Folders(ctx)
	_, _ = c.Note(ctx, "UUID")
	if len(seen) == 0 {
		t.Fatal("no requests were made")
	}
	for i, got := range seen {
		if got != "Bearer "+testToken {
			t.Errorf("request %d sent %q", i, got)
		}
	}
}

// Property: a server error is surfaced as an error, never as an empty result.
// Silently returning "no notes" when the Mac is unreachable would be the worst
// possible failure for a client that then rewrites something.
func TestServerFailuresAreNeverEmptyResults(t *testing.T) {
	for _, tc := range []struct {
		name string
		h    http.HandlerFunc
	}{
		{"500", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }},
		{"401", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }},
		{"html error page", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html>gateway</html>"))
		}},
		{"truncated json", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"uuid":"a"`))
		}},
		{"empty body", func(w http.ResponseWriter, r *http.Request) {}},
	} {
		srv := httptest.NewServer(tc.h)
		notes, err := New(srv.URL, testToken).Notes(context.Background(), ListOptions{})
		srv.Close()
		if err == nil {
			t.Errorf("%s: got %d notes and no error", tc.name, len(notes))
		}
	}
}

// Property: a refused rewrite is distinguishable from every other failure, so a
// caller can offer force rather than guessing.
func TestRefusedRewriteIsTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":"contains attachments","destroys":["attachments"]}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL, testToken).Replace(context.Background(), "UUID", "x", false, false)
	var refused *RefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("got %v, want a RefusedError", err)
	}
	if len(refused.Destroys) != 1 || refused.Destroys[0] != "attachments" {
		t.Errorf("did not carry what would be lost: %+v", refused)
	}
}

// Property: an unreachable Mac fails quickly rather than hanging a TUI.
func TestUnreachableServerFailsPromptly(t *testing.T) {
	c := New("http://127.0.0.1:1", testToken) // nothing listens on port 1
	c.HTTP.Timeout = 2 * time.Second
	start := time.Now()
	if _, err := c.Notes(context.Background(), ListOptions{}); err == nil {
		t.Fatal("expected an error")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %s to fail", d)
	}
}

// Property: a cancelled context stops the request.
func TestContextCancellationIsHonoured(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := New(srv.URL, testToken).Notes(ctx, ListOptions{}); err == nil {
		t.Fatal("expected an error")
	}
}

// Property: a write reports the flattening the server reported, so the Linux
// side can warn with the same words the Mac side would.
func TestDegradedFormattingIsCarriedBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"accepted":true,"degraded":["underlining"]}`))
	}))
	defer srv.Close()

	degraded, err := New(srv.URL, testToken).Replace(context.Background(), "UUID", "x", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(degraded) != 1 || degraded[0] != "underlining" {
		t.Errorf("got %v", degraded)
	}
}

// Property: a hostile or broken server cannot make the client allocate without
// bound. The body here is valid JSON, so only the size cap can reject it --
// an earlier version of this test sent malformed JSON and passed because of
// that rather than because of the cap.
func TestResponseSizeIsBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"uuid":"`))
		chunk := strings.Repeat("a", 1<<20)
		for i := 0; i < MaxResponse/len(chunk)+2; i++ {
			if _, err := w.Write([]byte(chunk)); err != nil {
				return
			}
		}
		w.Write([]byte(`"}]`))
	}))
	defer srv.Close()

	_, err := New(srv.URL, testToken).Notes(context.Background(), ListOptions{})
	if err == nil {
		t.Fatal("an unbounded response was accepted")
	}
	if !strings.Contains(err.Error(), "more than") {
		t.Errorf("rejected for the wrong reason: %v", err)
	}
}

const testToken = "test-token"
