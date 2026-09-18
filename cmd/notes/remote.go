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
