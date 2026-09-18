package main

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// The dispatch is tested because nothing else can catch it. -force has already
// regressed once to a parameter that was registered, documented, threaded in
// and never read, which compiles and vets clean.

func TestForceReachesTheUnguardedPath(t *testing.T) {
	db := fixture(t)
	// Without -force the guard refuses, naming what would be lost.
	out := runCLI(t, db, "new body", "replace", "UUID-ATTACH")
	if out.code != 1 || !strings.Contains(out.stderr, "attachments") {
		t.Errorf("without -force: code %d, stderr %q; want a refusal naming attachments", out.code, out.stderr)
	}
	// With -force the guard is skipped, so it gets as far as the Apple Event,
	// which fails here because osascript does not exist. What matters is that
	// it is a different failure: the refusal is gone.
	forced := runCLI(t, db, "new body", "replace", "-force", "UUID-ATTACH")
	if strings.Contains(forced.stderr, "attachments") {
		t.Errorf("-force did not reach the unguarded path: %q", forced.stderr)
	}
}

// The inverse mutation is the dangerous one: forcing unconditionally would
// destroy attachments on every replace.
func TestReplaceWithoutForceNeverSkipsTheGuard(t *testing.T) {
	out := runCLI(t, fixture(t), "new body", "replace", "UUID-ATTACH")
	if !strings.Contains(out.stderr, "attachments") {
		t.Fatalf("a bare replace bypassed the guard: %q", out.stderr)
	}
}

func TestForceIsRejectedWhereItMeansNothing(t *testing.T) {
	for _, cmd := range []string{"append", "rm", "list"} {
		out := runCLI(t, fixture(t), "", cmd, "-force", "UUID-PLAIN")
		if out.code != 2 || !strings.Contains(out.stderr, "-force only applies to replace") {
			t.Errorf("%s -force: code %d, stderr %q", cmd, out.code, out.stderr)
		}
	}
}

func TestEmptyBodyIsRefused(t *testing.T) {
	for _, cmd := range [][]string{{"new"}, {"replace", "UUID-PLAIN"}, {"append", "UUID-PLAIN"}} {
		out := runCLI(t, fixture(t), "   \n", cmd...)
		if out.code == 0 || !strings.Contains(out.stderr, "refusing") {
			t.Errorf("%v with blank stdin: code %d, stderr %q", cmd, out.code, out.stderr)
		}
	}
}

// Trashed, not Deleted: a note reaches the trash by more than one route, and
// the marker must reflect all of them.
func TestListMarksTrashedNotes(t *testing.T) {
	out := runCLI(t, fixture(t), "", "list", "-deleted")
	if !strings.Contains(out.stdout, "UUID-TRASHFOLDER") || !strings.Contains(out.stdout, "[deleted]") {
		t.Errorf("a note in the trash folder is not marked: %q", out.stdout)
	}
}

func TestExitCodes(t *testing.T) {
	db := fixture(t)
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"no args", nil, 2},
		{"unknown command", []string{"nope"}, 2},
		{"stray argument", []string{"list", "extra"}, 2},
		{"help", []string{"help"}, 0},
		{"missing note", []string{"show", "UUID-NOSUCH"}, 1},
		{"folders", []string{"folders"}, 0},
	} {
		if got := runCLI(t, db, "", tc.args...).code; got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

// --- harness -----------------------------------------------------------------

type cliResult struct {
	code           int
	stdout, stderr string
}

func runCLI(t *testing.T, db, stdin string, args ...string) cliResult {
	t.Helper()
	var out, errb bytes.Buffer
	full := args
	if len(args) > 0 {
		full = append([]string{args[0], "-db", db}, args[1:]...)
	}
	code := run(full, strings.NewReader(stdin), &out, &errb)
	return cliResult{code: code, stdout: out.String(), stderr: errb.String()}
}

func fixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "NoteStore.sqlite")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE ZICCLOUDSYNCINGOBJECT (Z_PK INTEGER PRIMARY KEY, ZIDENTIFIER TEXT,
			ZTITLE1 TEXT, ZTITLE2 TEXT, ZSNIPPET TEXT, ZFOLDER INTEGER, ZNOTEDATA INTEGER,
			ZCREATIONDATE1 REAL, ZCREATIONDATE REAL, ZCREATIONDATE2 REAL,
			ZMODIFICATIONDATE1 REAL, ZMODIFICATIONDATE REAL,
			ZMARKEDFORDELETION INTEGER, ZISPINNED INTEGER, ZISPASSWORDPROTECTED INTEGER)`,
		`CREATE TABLE ZICNOTEDATA (Z_PK INTEGER PRIMARY KEY, ZNOTE INTEGER, ZDATA BLOB)`,
		`CREATE TABLE Z_METADATA (Z_UUID TEXT)`,
		`INSERT INTO Z_METADATA VALUES ('STORE-UUID')`,
		`INSERT INTO ZICCLOUDSYNCINGOBJECT (Z_PK, ZIDENTIFIER, ZTITLE2) VALUES
			(1, 'DefaultFolder-CloudKit', 'Notes'), (2, 'TrashFolder-CloudKit', 'Recently Deleted')`,
		`INSERT INTO ZICCLOUDSYNCINGOBJECT (Z_PK, ZIDENTIFIER, ZTITLE1, ZFOLDER, ZNOTEDATA, ZMODIFICATIONDATE1) VALUES
			(10, 'UUID-PLAIN', 'Plain', 1, 100, 500000),
			(11, 'UUID-ATTACH', 'Has attachment', 1, 101, 400000),
			(12, 'UUID-TRASHFOLDER', 'In the trash folder', 2, 102, 300000)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	for _, r := range []struct {
		pk, note int
		attach   bool
	}{{100, 10, false}, {101, 11, true}, {102, 12, false}} {
		if _, err := db.Exec(`INSERT INTO ZICNOTEDATA VALUES (?, ?, ?)`, r.pk, r.note, blob(t, r.attach)); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	return path
}

func blob(t *testing.T, attach bool) []byte {
	t.Helper()
	varint := func(v uint64) []byte {
		var b []byte
		for v >= 0x80 {
			b = append(b, byte(v)|0x80)
			v >>= 7
		}
		return append(b, byte(v))
	}
	fld := func(n int, d []byte) []byte {
		return append(append(varint(uint64(n)<<3|2), varint(uint64(len(d)))...), d...)
	}
	vfld := func(n int, v uint64) []byte { return append(varint(uint64(n)<<3), varint(v)...) }

	run := vfld(1, 4)
	if attach {
		run = append(run, fld(12, append(fld(1, []byte("ATT")), fld(2, []byte("public.jpeg"))...))...)
	}
	raw := fld(2, append(vfld(2, 1), fld(3, append(fld(2, []byte("text")), fld(5, run)...))...))
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(raw)
	zw.Close()
	return buf.Bytes()
}

// A status bar redraws on a timer and reads one JSON object per run. A module
// that exits non-zero, prints nothing, or prints something unparseable leaves a
// blank slot with no explanation -- so these hold even when the Mac is asleep.
func TestBarAlwaysEmitsOneValidJSONObject(t *testing.T) {
	for _, tc := range []struct{ name, db, server string }{
		{"local database", fixture(t), ""},
		{"unreachable server", "", "http://127.0.0.1:1"},
		{"missing database", filepath.Join(t.TempDir(), "absent.sqlite"), ""},
	} {
		args := []string{"bar"}
		if tc.server != "" {
			args = append(args, "-server", tc.server, "-token", "t")
		}
		var out, errb bytes.Buffer
		code := run(append(args[:1:1], args[1:]...), strings.NewReader(""), &out, &errb)
		if tc.db != "" {
			out.Reset()
			errb.Reset()
			code = run([]string{"bar", "-db", tc.db}, strings.NewReader(""), &out, &errb)
		}
		if code != 0 {
			t.Errorf("%s: exit %d, stderr %q", tc.name, code, errb.String())
		}
		var got map[string]any
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Errorf("%s: not JSON: %q", tc.name, out.String())
			continue
		}
		if _, ok := got["text"]; !ok {
			t.Errorf("%s: no text field: %q", tc.name, out.String())
		}
		if _, ok := got["class"]; !ok {
			t.Errorf("%s: no class field: %q", tc.name, out.String())
		}
	}
}

// The token must never reach the bar's tooltip, which ends up on screen and in
// the bar's own logs.
func TestBarNeverLeaksTheToken(t *testing.T) {
	const secret = "supersecrettoken"
	var out, errb bytes.Buffer
	run([]string{"bar", "-server", "http://127.0.0.1:1", "-token", secret},
		strings.NewReader(""), &out, &errb)
	if strings.Contains(out.String()+errb.String(), secret) {
		t.Errorf("token leaked: %q %q", out.String(), errb.String())
	}
}

// An unreachable Mac is the normal case, not a failure worth blocking on.
func TestBarFailsFast(t *testing.T) {
	start := time.Now()
	var out bytes.Buffer
	run([]string{"bar", "-server", "http://127.0.0.1:1", "-token", "t"}, strings.NewReader(""), &out, io.Discard)
	if d := time.Since(start); d > barTimeout*2 {
		t.Errorf("took %s", d)
	}
}
