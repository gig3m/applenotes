package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// Where it listens is the security boundary, so the refusal is tested rather
// than left to a log line nobody reads.
func TestNonLoopbackRequiresOptIn(t *testing.T) {
	for _, tc := range []struct {
		addr      string
		listenAll bool
		wantErr   bool
	}{
		{"127.0.0.1:8437", false, false},
		{"[::1]:8437", false, false},
		{"localhost:8437", false, false},
		{":8437", false, false},
		{"0.0.0.0:8437", false, true},
		{"100.64.0.1:8437", false, true},
		{"0.0.0.0:8437", true, false},
		{"100.64.0.1:8437", true, false},
		{"garbage", false, true},
	} {
		err := checkAddr(tc.addr, tc.listenAll)
		if (err != nil) != tc.wantErr {
			t.Errorf("checkAddr(%q, %v) = %v, wantErr %v", tc.addr, tc.listenAll, err, tc.wantErr)
		}
	}
}

// The refusal must say what to do instead, not just decline.
func TestRefusalSuggestsTailscaleServe(t *testing.T) {
	err := checkAddr("0.0.0.0:9999", false)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{"tailscale serve", "9999", "-listen-all"} {
		if !contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q: %v", want, err)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// A body cap limits bytes, not time. Without a read timeout an authenticated
// client that dribbles a body holds a goroutine and a connection indefinitely,
// and WriteTimeout does not unblock a handler stuck reading.
func TestSlowBodyIsCutOff(t *testing.T) {
	srv := &http.Server{
		ReadHeaderTimeout: time.Second,
		ReadTimeout:       500 * time.Millisecond,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body) // a handler that reads the whole body
			w.WriteHeader(http.StatusOK)
		}),
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go srv.Serve(ln)
	defer srv.Close()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 1000\r\n\r\n")

	done := make(chan struct{})
	go func() {
		defer close(done)
		io.Copy(io.Discard, conn) // blocks until the server gives up
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("the server did not cut off a dribbled body")
	}
}
