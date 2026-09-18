// Command notesd serves this Mac's Apple Notes over HTTP.
//
// It is meant to run as a LaunchAgent in a logged-in GUI session, reachable
// only over Tailscale, on a Mac kept for the purpose. It reads the notes
// database directly and writes through Notes.app.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gig3m/applenotes/internal/notesd"
	"github.com/gig3m/applenotes/internal/notestore"
)

func main() {
	var (
		addr      = flag.String("addr", "127.0.0.1:8437", "address to listen on")
		dbPath    = flag.String("db", "", "path to NoteStore.sqlite (default: the current user's)")
		tokenPath = flag.String("token", defaultTokenPath(), "file holding the bearer token; created if absent")
		listenAll = flag.Bool("listen-all", false, "permit binding to an address other than loopback (see -h)")
	)
	flag.Usage = usage
	flag.Parse()

	if err := run(*addr, *dbPath, *tokenPath, *listenAll); err != nil {
		log.Fatalln("notesd:", err)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: notesd [flags]

Serves this Mac's Apple Notes over HTTP. The bearer token is the only
perimeter, so where it listens matters.

Reaching it from another machine, in order of preference:

  tailscale serve --bg 8437      notesd stays on loopback; Tailscale terminates
                                 TLS and only tailnet peers can reach it.

  -addr <tailscale-ip>:8437      bind the tailnet interface directly. Requires
                                 -listen-all. Reachable only over the tailnet.

  -addr 0.0.0.0:8437             every interface, including whatever Wi-Fi this
                                 Mac joins next. Requires -listen-all, and you
                                 should not want this.

flags:
`)
	flag.PrintDefaults()
}

func run(addr, dbPath, tokenPath string, listenAll bool) error {
	if err := checkAddr(addr, listenAll); err != nil {
		return err
	}
	token, err := loadOrCreateToken(tokenPath)
	if err != nil {
		return err
	}
	store, err := notestore.Open(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	srv := &http.Server{
		Addr:              addr,
		Handler:           notesd.New(store, token).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// Bytes are capped per request, but bytes are not time: without this a
		// client that dribbles a body one byte at a time pins a goroutine and a
		// connection indefinitely. WriteTimeout does not unblock a handler
		// stuck reading.
		ReadTimeout: 30 * time.Second,
		// Generous: an Apple Event is slow, and a consent prompt makes it
		// slower. The handler's own context bounds the work.
		WriteTimeout: 3 * time.Minute,
		IdleTimeout:  2 * time.Minute,
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	log.Printf("notesd: listening on %s, token in %s", ln.Addr(), tokenPath)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	}
}

// checkAddr refuses a non-loopback bind without an explicit opt-in. A logged
// warning is not enough: 0.0.0.0 means every network this Mac ever joins, and
// the token is the only thing in the way.
func checkAddr(addr string, listenAll bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("bad -addr %q: %w", addr, err)
	}
	switch host {
	case "127.0.0.1", "::1", "localhost", "":
		return nil
	}
	if !listenAll {
		return fmt.Errorf("refusing to listen on %s without -listen-all.\n"+
			"       Prefer leaving notesd on loopback and running:  tailscale serve --bg %s\n"+
			"       See -h for why", host, portOf(addr))
	}
	if host == "0.0.0.0" || host == "::" {
		log.Printf("notesd: listening on %s -- every interface, including whatever network "+
			"this Mac joins next. The token is the only thing in the way.", host)
	}
	return nil
}

func portOf(addr string) string {
	if _, port, err := net.SplitHostPort(addr); err == nil {
		return port
	}
	return "8437"
}

func defaultTokenPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "notesd.token"
	}
	return filepath.Join(home, ".config", "applenotes", "token")
}

// loadOrCreateToken reads the bearer token, generating one on first run. The
// file is 0600 and its directory 0700: it is the only thing standing between
// the network and every note on this Mac.
func loadOrCreateToken(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		token := string(trimSpace(b))
		if token == "" {
			return "", fmt.Errorf("token file %s is empty", path)
		}
		if err := checkPerms(path); err != nil {
			return "", err
		}
		return token, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	log.Printf("notesd: generated a token in %s", path)
	return token, nil
}

func checkPerms(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("token file %s is readable by other users (mode %o); chmod 600 it", path, fi.Mode().Perm())
	}
	return nil
}

func trimSpace(b []byte) []byte { return bytes.TrimSpace(b) }
