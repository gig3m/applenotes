package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/gig3m/applenotes/internal/client"
	"github.com/gig3m/applenotes/internal/notestore"
)

// barOutput is what waybar and the Omarchy bar read: one JSON object per run.
type barOutput struct {
	Text    string `json:"text"`
	Tooltip string `json:"tooltip"`
	Class   string `json:"class"`
	Alt     string `json:"alt,omitempty"`
}

// barTimeout is short on purpose. The bar redraws on a timer and the Mac is
// often asleep; a module that blocks holds up the whole bar.
const barTimeout = 3 * time.Second

// bar prints one line of JSON for a status bar. It always prints valid JSON,
// including when the Mac is unreachable -- a module that exits non-zero or
// prints nothing leaves a blank slot in the bar with no explanation.
func bar(stdout io.Writer, dbPath, server, token, folder string) error {
	out := barOutput{Text: "notes", Class: "ok"}

	ctx, cancel := context.WithTimeout(context.Background(), barTimeout)
	defer cancel()

	notes, err := barNotes(ctx, dbPath, server, token, folder)
	switch {
	case err != nil:
		// The Mac being asleep is the normal case, not an error worth shouting
		// about. The class lets the bar style it differently.
		out.Text = "notes ?"
		out.Class = "unreachable"
		out.Tooltip = "cannot reach notes: " + firstLine(err.Error())
	case len(notes) == 0:
		out.Text = "notes 0"
		out.Tooltip = "no notes"
	default:
		sort.Slice(notes, func(i, j int) bool { return notes[i].Modified.After(notes[j].Modified) })
		out.Text = fmt.Sprintf("notes %d", len(notes))
		var b strings.Builder
		fmt.Fprintf(&b, "%d notes, most recent:", len(notes))
		for i, n := range notes {
			if i == 5 {
				break
			}
			title := n.Title
			if title == "" {
				title = "(untitled)"
			}
			fmt.Fprintf(&b, "\n  %s  %s", n.Modified.Local().Format("Jan 2 15:04"), truncate(title, 48))
		}
		out.Tooltip = b.String()
		out.Alt = notes[0].UUID
	}

	enc := json.NewEncoder(stdout)
	return enc.Encode(out)
}

// barNote is the little the bar needs, from either source.
type barNote struct {
	UUID     string
	Title    string
	Modified time.Time
}

func barNotes(ctx context.Context, dbPath, server, token, folder string) ([]barNote, error) {
	if server != "" {
		c, err := remoteClient(server, token)
		if err != nil {
			return nil, err
		}
		c.HTTP.Timeout = barTimeout
		ns, err := c.Notes(ctx, client.ListOptions{Folder: folder})
		if err != nil {
			return nil, err
		}
		out := make([]barNote, 0, len(ns))
		for _, n := range ns {
			out = append(out, barNote{UUID: n.UUID, Title: n.Title, Modified: n.Modified})
		}
		return out, nil
	}

	s, err := open(dbPath)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	ns, err := s.Notes(notestore.ListOptions{Folder: folder})
	if err != nil {
		return nil, err
	}
	out := make([]barNote, 0, len(ns))
	for _, n := range ns {
		out = append(out, barNote{UUID: n.UUID, Title: n.Title, Modified: n.Modified})
	}
	return out, nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
