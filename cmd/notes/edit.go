package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/gig3m/applenotes/internal/client"
	"github.com/gig3m/applenotes/internal/edit"
	"github.com/gig3m/applenotes/internal/notesapp"
	"github.com/gig3m/applenotes/internal/notestore"
)

// localStore and remoteStore adapt the two sources to edit.Store, so the same
// editor works against the Mac's own database and against notesd.

type localStore struct {
	store  *notestore.Store
	writer *notesapp.Writer
}

func (l localStore) Fetch(ctx context.Context, uuid string) (string, error) {
	body, err := l.store.Body(uuid)
	if err != nil {
		return "", err
	}
	return body.Markdown(), nil
}

func (l localStore) Write(ctx context.Context, uuid, markdown string, force bool) ([]string, error) {
	if force {
		return nil, l.writer.ReplaceForce(ctx, uuid, markdown)
	}
	return l.writer.Replace(ctx, uuid, markdown)
}

type remoteStore struct{ c *client.Client }

func (r remoteStore) Fetch(ctx context.Context, uuid string) (string, error) {
	n, err := r.c.Note(ctx, uuid)
	if err != nil {
		return "", err
	}
	if n.Locked {
		return "", fmt.Errorf("%s is password-protected; its body is not readable", n.Title)
	}
	return n.Markdown, nil
}

func (r remoteStore) Write(ctx context.Context, uuid, markdown string, force bool) ([]string, error) {
	return r.c.Replace(ctx, uuid, markdown, force)
}

func editNote(stderr io.Writer, dbPath, server, token, uuid string, force bool) error {
	ed := &edit.Editor{
		Force:     force,
		OnDegrade: func(f []string) { warnDegraded(stderr, f) },
		Run:       edit.Runner(stderr),
	}
	if server != "" {
		c, err := remoteClient(server, token)
		if err != nil {
			return err
		}
		ed.Store = remoteStore{c: c}
	} else {
		s, w, err := writer(dbPath)
		if err != nil {
			return err
		}
		defer s.Close()
		ed.Store = localStore{store: s, writer: w}
	}
	wrote, err := ed.Edit(context.Background(), uuid)
	if err != nil {
		return err
	}
	if !wrote {
		fmt.Fprintln(stderr, "notes: no changes; nothing was written")
		return nil
	}
	warnLag(stderr)
	return nil
}

// captureNote makes a note from a single line, for a launcher or a bar widget
// where opening an editor would be too much ceremony.
func captureNote(stdin io.Reader, stdout, stderr io.Writer, dbPath, server, token, folder, text string) error {
	if strings.TrimSpace(text) == "" {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		text = string(b)
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("nothing to capture")
	}

	ctx := context.Background()
	var uuid string
	var err error
	if server != "" {
		c, cerr := remoteClient(server, token)
		if cerr != nil {
			return cerr
		}
		uuid, err = c.Create(ctx, folder, text)
	} else {
		s, w, werr := writer(dbPath)
		if werr != nil {
			return werr
		}
		defer s.Close()
		uuid, err = w.Create(ctx, folder, text)
	}
	if err != nil && !strings.Contains(err.Error(), "not yet") {
		return err
	}
	if uuid != "" {
		fmt.Fprintln(stdout, uuid)
	}
	warnLag(stderr)
	return nil
}
