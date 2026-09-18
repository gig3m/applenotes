package notesapp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gig3m/applenotes/internal/applescript"
)

// What actually reaches osascript was previously unobservable: the conversion
// to HTML could be deleted from every write path, and the note id could be
// empty or malformed, with no test failing. These assert the payload.

type capture struct {
	src    string
	args   []string
	result string
	err    error
}

func (c *capture) Run(ctx context.Context, src string, args ...string) (string, error) {
	c.src, c.args = src, args
	return c.result, c.err
}

func writerWith(t *testing.T, c *capture) *Writer {
	t.Helper()
	w := New(openFixture(t))
	w.Runner = c
	return w
}

// The body must arrive as the HTML dialect Notes accepts, not as Markdown.
func TestWritesSendHTMLNotMarkdown(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*Writer) error
	}{
		{"create", func(w *Writer) error {
			_, err := w.Create(context.Background(), "", "# Heading\n\n- bullet")
			return err
		}},
		{"replace", func(w *Writer) error {
			_, err := w.Replace(context.Background(), "UUID-PLAIN", "# Heading\n\n- bullet")
			return err
		}},
	} {
		c := &capture{result: "x-coredata://STORE-UUID/ICNote/p10"}
		_ = tc.call(writerWith(t, c))
		body := c.args[len(c.args)-1]
		if !strings.Contains(body, "<h1>Heading</h1>") || !strings.Contains(body, "<li>bullet</li>") {
			t.Errorf("%s sent %q, want converted HTML", tc.name, body)
		}
		if strings.Contains(body, "# Heading") {
			t.Errorf("%s sent raw Markdown: %q", tc.name, body)
		}
	}
}

// The note id must be the x-coredata form AppleScript addresses notes by, built
// from the store UUID and the row id. An empty or wrongly-shaped id would fail
// silently against a real Notes.app.
func TestWritesSendTheCoreDataID(t *testing.T) {
	c := &capture{}
	w := writerWith(t, c)
	_, _ = w.Replace(context.Background(), "UUID-PLAIN", "body")

	// UUID-PLAIN is row 2 in the fixture.
	const want = "x-coredata://STORE-UUID/ICNote/p2"
	if len(c.args) == 0 || c.args[0] != want {
		t.Errorf("sent id %q, want %q", c.args, want)
	}
}

// A folder UUID is not a note and must never be formatted as a note id.
func TestFolderUUIDIsNotAddressable(t *testing.T) {
	c := &capture{}
	w := writerWith(t, c)
	_, err := w.Replace(context.Background(), "FOLDER-UUID", "body")
	if err == nil {
		t.Fatal("a folder UUID was accepted as a note")
	}
	if c.src != "" {
		t.Errorf("a script was sent for a folder: %q", c.args)
	}
}

// Append composes existing + "\n" + new. Without the join the appended text
// fuses onto the note's last line.
func TestAppendComposesWithANewline(t *testing.T) {
	c := &capture{}
	w := writerWith(t, c)
	if _, err := w.Append(context.Background(), "UUID-PLAIN", "added"); err != nil {
		t.Fatal(err)
	}
	body := c.args[len(c.args)-1]
	// The fixture note's text is "text"; the two must not run together.
	if strings.Contains(body, "textadded") {
		t.Errorf("appended text fused onto the last line: %q", body)
	}
	if !strings.Contains(body, "added") {
		t.Errorf("appended text missing: %q", body)
	}
}

// A freshly created note whose row is not in the database yet has no portable
// UUID, and saying so beats inventing one that nothing accepts.
func TestCreateReportsNotYetVisible(t *testing.T) {
	c := &capture{result: "x-coredata://STORE-UUID/ICNote/p9999"}
	_, err := writerWith(t, c).Create(context.Background(), "", "body")
	if !errors.Is(err, ErrNotYetVisible) {
		t.Errorf("got %v, want ErrNotYetVisible", err)
	}
}

// A created note whose row is present comes back as the portable UUID, not the
// machine-local id.
func TestCreateMapsBackToThePortableUUID(t *testing.T) {
	c := &capture{result: "x-coredata://STORE-UUID/ICNote/p2"}
	uuid, err := writerWith(t, c).Create(context.Background(), "", "body")
	if err != nil {
		t.Fatal(err)
	}
	if uuid != "UUID-PLAIN" {
		t.Errorf("got %q, want the ZIDENTIFIER UUID", uuid)
	}
	if strings.HasPrefix(uuid, "x-coredata") {
		t.Error("returned the machine-local id, which no command accepts")
	}
}

// Notes.app answers with a trailing newline; without trimming it the id fails
// to parse and every create errors.
func TestCreateToleratesTrailingWhitespaceFromOsascript(t *testing.T) {
	c := &capture{result: "x-coredata://STORE-UUID/ICNote/p2\n"}
	w := writerWith(t, c)
	w.Runner = applescript.RunnerFunc(func(ctx context.Context, src string, args ...string) (string, error) {
		return strings.TrimSpace(c.result), nil // what applescript.Run does
	})
	if _, err := w.Create(context.Background(), "", "body"); err != nil {
		t.Errorf("a trailing newline broke create: %v", err)
	}
}
