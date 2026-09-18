package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/gig3m/applenotes/internal/client"
	"github.com/gig3m/applenotes/internal/notestore"
)

// The remote commands mirror the local ones against notesd, so the same binary
// works on a Linux machine that has no notes database of its own.

func remoteClient(server, token string) (*client.Client, error) {
	if token == "" {
		token = os.Getenv("NOTESD_TOKEN")
	}
	if token == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			b, err := os.ReadFile(filepath.Join(home, ".config", "applenotes", "token"))
			if err == nil {
				token = strings.TrimSpace(string(b))
			}
		}
	}
	if token == "" {
		return nil, fmt.Errorf("no token: pass -token, set $NOTESD_TOKEN, or put it in ~/.config/applenotes/token")
	}
	return client.New(server, token), nil
}

func remoteList(stdout io.Writer, server, token, folder string, deleted bool) error {
	c, err := remoteClient(server, token)
	if err != nil {
		return err
	}
	notes, err := c.Notes(context.Background(), client.ListOptions{Folder: folder, IncludeDeleted: deleted})
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "MODIFIED\tFOLDER\tUUID\tTITLE")
	for _, n := range notes {
		title := n.Title
		if title == "" {
			title = "(untitled)"
		}
		if n.Locked {
			title += "  [locked]"
		}
		if n.SharedWithMe {
			title += "  [theirs]"
		} else if n.Shared {
			title += "  [shared]"
		}
		if n.Trashed {
			title += "  [deleted]"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			n.Modified.Local().Format("2006-01-02 15:04"), n.Folder, n.UUID, title)
	}
	return w.Flush()
}

func remoteFolders(stdout io.Writer, server, token string) error {
	c, err := remoteClient(server, token)
	if err != nil {
		return err
	}
	fs, err := c.Folders(context.Background())
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tUUID")
	for _, f := range fs {
		fmt.Fprintf(w, "%s\t%s\n", f.Name, f.UUID)
	}
	return w.Flush()
}

func remoteShow(stdout io.Writer, server, token, uuid string) error {
	c, err := remoteClient(server, token)
	if err != nil {
		return err
	}
	n, err := c.Note(context.Background(), uuid)
	if err != nil {
		return err
	}
	if n.Locked {
		return fmt.Errorf("%s is password-protected; its body is not readable", n.Title)
	}
	fmt.Fprintln(stdout, n.Markdown)
	return nil
}

func search(stdout io.Writer, dbPath, server, token, folder, query string, deleted bool) error {
	type row struct{ modified, folder, uuid, title, context string }
	var rows []row

	if server != "" {
		c, err := remoteClient(server, token)
		if err != nil {
			return err
		}
		hits, err := c.Search(context.Background(), query, folder, deleted)
		if err != nil {
			return err
		}
		for _, h := range hits {
			rows = append(rows, row{h.Modified.Local().Format("2006-01-02 15:04"), h.Folder, h.UUID, h.Title, h.Context})
		}
	} else {
		s, err := notestore.Open(dbPath)
		if err != nil {
			return err
		}
		defer s.Close()
		hits, err := s.Search(notestore.SearchOptions{Query: query, Folder: folder, IncludeDeleted: deleted})
		if err != nil {
			return err
		}
		for _, h := range hits {
			rows = append(rows, row{h.Modified.Local().Format("2006-01-02 15:04"), h.FolderName, h.UUID, h.Title, h.Context})
		}
	}

	if len(rows) == 0 {
		fmt.Fprintf(stdout, "no notes contain %q\n", query)
		return nil
	}
	// One note per stanza rather than a table: the context is the point, and a
	// column would truncate it to uselessness.
	for _, r := range rows {
		title := r.title
		if title == "" {
			title = "(untitled)"
		}
		fmt.Fprintf(stdout, "%s  %s\n  %s  %s\n  %s\n\n", r.modified, title, r.folder, r.uuid, r.context)
	}
	return nil
}

// The write commands below exist because the machine driving this is usually
// not the Mac holding the notes: without them, creating or deleting a note from
// Linux fails on a database that was never going to be there.

// readBody reads Markdown from stdin and refuses an empty one, so a mistyped
// pipe cannot silently blank a note.
func readBody(stdin io.Reader, verb string) (string, error) {
	md, err := io.ReadAll(stdin)
	if err != nil {
		return "", err
	}
	if len(strings.TrimSpace(string(md))) == 0 {
		return "", fmt.Errorf("refusing to %s an empty body", verb)
	}
	return string(md), nil
}

func remoteNew(stdin io.Reader, stdout, stderr io.Writer, server, token, folder string) error {
	md, err := readBody(stdin, "create a note with")
	if err != nil {
		return err
	}
	c, err := remoteClient(server, token)
	if err != nil {
		return err
	}
	uuid, err := c.Create(context.Background(), folder, md)
	if err != nil {
		return err
	}
	// The daemon reports the note created but not yet in the database as an
	// empty UUID; saying nothing would look like a silent failure.
	if uuid == "" {
		fmt.Fprintln(stderr,
			"notes: the note was created, but Notes.app has not written it to the\n"+
				"       database yet, so its UUID is not known. Run 'notes list' later.")
		return nil
	}
	fmt.Fprintln(stdout, uuid)
	warnLag(stderr)
	return nil
}

func remoteAppend(stdin io.Reader, stderr io.Writer, server, token, uuid string, allowShared bool) error {
	md, err := readBody(stdin, "append")
	if err != nil {
		return err
	}
	c, err := remoteClient(server, token)
	if err != nil {
		return err
	}
	degraded, err := c.Append(context.Background(), uuid, md, allowShared)
	if err != nil {
		return err
	}
	warnDegraded(stderr, degraded)
	warnLag(stderr)
	return nil
}

func remoteReplace(stdin io.Reader, stderr io.Writer, server, token, uuid string, force, allowShared bool) error {
	md, err := readBody(stdin, "replace a note with")
	if err != nil {
		return err
	}
	c, err := remoteClient(server, token)
	if err != nil {
		return err
	}
	degraded, err := c.Replace(context.Background(), uuid, md, force, allowShared)
	if err != nil {
		return err
	}
	warnDegraded(stderr, degraded)
	warnLag(stderr)
	return nil
}

func remoteRm(stderr io.Writer, server, token, uuid string, allowShared bool) error {
	c, err := remoteClient(server, token)
	if err != nil {
		return err
	}
	if err := c.Delete(context.Background(), uuid, allowShared); err != nil {
		return err
	}
	fmt.Fprintln(stderr, "notes: moved to Recently Deleted.")
	return nil
}
