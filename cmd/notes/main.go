// Command notes reads and writes Apple Notes on this Mac.
//
// Reads come from the local NoteStore database, which is opened read-only.
// Writes go through Apple Events to Notes.app, which owns that file.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/gig3m/applenotes/internal/notesapp"
	"github.com/gig3m/applenotes/internal/notestore"
)

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

// run is main's body with its streams and exit code injected, so the dispatch
// can be tested. The -force wiring in particular has regressed to a dead
// parameter once already, and nothing outside a test can catch that.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) (code int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintln(stderr, "notes: internal error:", r)
			code = 1
		}
	}()
	if len(args) < 1 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	cmd := args[0]

	// Flags are parsed per subcommand rather than globally: the stdlib flag
	// package stops at the first non-flag argument, so a global FlagSet would
	// silently ignore everything after the command name.
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(stdout, usageText) } // -h is not an error
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "", "path to NoteStore.sqlite (default: the current user's)")
	// Registered for every subcommand rather than only for list. Gating the
	// allocation on the command name leaves nil pointers that the next alias or
	// refactor dereferences.
	folder := fs.String("folder", "", "list: limit to a folder, by name or UUID")
	deleted := fs.Bool("deleted", false, "list: include notes in Recently Deleted")
	server := fs.String("server", os.Getenv("NOTESD_URL"), "notesd URL; reads and writes go over HTTP instead of the local database")
	token := fs.String("token", "", "bearer token for -server (default: $NOTESD_TOKEN, or ~/.config/applenotes/token)")
	force := fs.Bool("force", false, "replace: overwrite even if it discards attachments or checklists")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	usageErr := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "notes: "+format+"\n\n", a...)
		fmt.Fprint(stderr, usageText)
		return 2
	}
	if *force && cmd != "replace" && cmd != "edit" {
		return usageErr("-force only applies to replace and edit")
	}

	var err error
	switch cmd {
	case "list":
		if fs.NArg() > 0 {
			return usageErr("list takes no arguments (did you mean -folder %s?)", fs.Arg(0))
		}
		if *server != "" {
			err = remoteList(stdout, *server, *token, *folder, *deleted)
			break
		}
		err = list(stdout, *dbPath, *folder, *deleted)
	case "folders":
		if fs.NArg() > 0 {
			return usageErr("folders takes no arguments")
		}
		if *server != "" {
			err = remoteFolders(stdout, *server, *token)
			break
		}
		err = folders(stdout, *dbPath)
	case "show":
		if fs.NArg() != 1 {
			return usageErr("show needs exactly one note UUID")
		}
		if *server != "" {
			err = remoteShow(stdout, *server, *token, fs.Arg(0))
			break
		}
		err = show(stdout, *dbPath, fs.Arg(0))
	case "new":
		if fs.NArg() > 0 {
			return usageErr("new reads Markdown from stdin and takes no arguments")
		}
		err = newNote(stdin, stdout, stderr, *dbPath, *folder)
	case "append":
		if fs.NArg() != 1 {
			return usageErr("append needs exactly one note UUID")
		}
		err = appendNote(stdin, stderr, *dbPath, fs.Arg(0))
	case "replace":
		if fs.NArg() != 1 {
			return usageErr("replace needs exactly one note UUID")
		}
		err = replaceNote(stdin, stderr, *dbPath, fs.Arg(0), *force)
	case "rm":
		if fs.NArg() != 1 {
			return usageErr("rm needs exactly one note UUID")
		}
		err = rmNote(stderr, *dbPath, fs.Arg(0))
	case "edit":
		if fs.NArg() != 1 {
			return usageErr("edit needs exactly one note UUID")
		}
		err = editNote(stderr, *dbPath, *server, *token, fs.Arg(0), *force)
	case "capture":
		err = captureNote(stdin, stdout, stderr, *dbPath, *server, *token, *folder, strings.Join(fs.Args(), " "))
	case "decode":
		if fs.NArg() > 0 {
			return usageErr("decode reads from stdin and takes no arguments")
		}
		err = decode(stdin, stdout)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usageText)
		return 0
	default:
		return usageErr("unknown command %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(stderr, "notes:", err)
		return 1
	}
	return 0
}

const usageText = `usage: notes <command> [flags]

commands:
  list [-folder NAME] [-deleted]   list notes, newest first
  folders                          list folders
  show <uuid>                      print one note as Markdown
  new [-folder NAME]               create a note from Markdown on stdin
  append <uuid>                    append Markdown from stdin to a note
  replace [-force] <uuid>          overwrite a note with Markdown from stdin
  edit [-force] <uuid>             open a note in $EDITOR and write it back
  capture [text…]                  make a note from one line, or from stdin
  rm <uuid>                        move a note to Recently Deleted
  decode                           decode a raw ZICNOTEDATA blob on stdin

common flags:
  -db PATH       path to NoteStore.sqlite (default: the current user's)
  -server URL    talk to notesd on a Mac instead of a local database
                 (default $NOTESD_URL; token from $NOTESD_TOKEN or
                 ~/.config/applenotes/token)

-folder matches a folder by name or UUID and does not descend into
subfolders. Notes in Recently Deleted are hidden unless -deleted is given.
`

func open(path string) (*notestore.Store, error) { return notestore.Open(path) }

func list(stdout io.Writer, path, folder string, deleted bool) error {
	s, err := open(path)
	if err != nil {
		return err
	}
	defer s.Close()

	notes, err := s.Notes(notestore.ListOptions{IncludeDeleted: deleted, Folder: folder})
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
		// Trashed, not Deleted: a note reaches the trash by more than one
		// route and the flag alone misses some of them.
		if n.Trashed {
			title += "  [deleted]"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			n.Modified.Local().Format("2006-01-02 15:04"), n.FolderName, n.UUID, title)
	}
	return w.Flush()
}

func folders(stdout io.Writer, path string) error {
	s, err := open(path)
	if err != nil {
		return err
	}
	defer s.Close()

	fs, err := s.Folders()
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

func show(stdout io.Writer, path, uuid string) error {
	s, err := open(path)
	if err != nil {
		return err
	}
	defer s.Close()

	meta, err := s.Meta(uuid)
	if err != nil {
		return err
	}
	if meta.Locked {
		return fmt.Errorf("%s is password-protected; its body is not readable", meta.Title)
	}
	body, err := s.Body(uuid)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, body.Markdown())
	return nil
}

// writer opens the store read-only and pairs it with the Apple Events writer.
// Writes never go through SQLite: the database handle is read-only by
// construction, and Notes.app owns its own file.
func writer(path string) (*notestore.Store, *notesapp.Writer, error) {
	s, err := open(path)
	if err != nil {
		return nil, nil, err
	}
	return s, notesapp.New(s), nil
}

func newNote(stdin io.Reader, stdout, stderr io.Writer, path, folder string) error {
	md, err := io.ReadAll(stdin)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(md)) == 0 {
		return fmt.Errorf("refusing to create an empty note")
	}
	s, w, err := writer(path)
	if err != nil {
		return err
	}
	defer s.Close()

	uuid, err := w.Create(context.Background(), folder, string(md))
	if errors.Is(err, notesapp.ErrNotYetVisible) {
		fmt.Fprintln(stderr,
			"notes: the note was created, but Notes.app has not written it to the\n"+
				"       database yet, so its UUID is not known. Run 'notes list' later.")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, uuid)
	warnLag(stderr)
	return nil
}

// warnDegraded reports formatting the rewrite flattened. Nothing is removed,
// but an indent becomes spaces in the text, so it is worth saying.
func warnDegraded(stderr io.Writer, features []string) {
	if len(features) == 0 {
		return
	}
	fmt.Fprintf(stderr,
		"notes: this rewrite flattened %s.\n"+
			"       No text is removed, though an indent becomes spaces in the text.\n",
		strings.Join(features, ", "))
}

// warnLag explains why a write may not show up in list or show yet.
func warnLag(stderr io.Writer) {
	fmt.Fprintln(stderr,
		"notes: written. Notes.app persists changes to its database on its own\n"+
			"       schedule, so this may not appear in list/show for a while.")
}

func appendNote(stdin io.Reader, stderr io.Writer, path, uuid string) error {
	md, err := io.ReadAll(stdin)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(md)) == 0 {
		return fmt.Errorf("refusing to append an empty body")
	}
	s, w, err := writer(path)
	if err != nil {
		return err
	}
	defer s.Close()

	degraded, err := w.Append(context.Background(), uuid, string(md))
	var lossy *notesapp.ErrLossyRewrite
	if errors.As(err, &lossy) {
		return fmt.Errorf("%w.\n"+
			"       Appending rewrites the whole note, and that content cannot\n"+
			"       survive the trip. Add to this note in Notes.app instead", err)
	}
	if err != nil {
		return err
	}
	warnDegraded(stderr, degraded)
	warnLag(stderr)
	return nil
}

func replaceNote(stdin io.Reader, stderr io.Writer, path, uuid string, force bool) error {
	md, err := io.ReadAll(stdin)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(md)) == 0 {
		return fmt.Errorf("refusing to replace a note with an empty body")
	}
	s, w, err := writer(path)
	if err != nil {
		return err
	}
	defer s.Close()

	if force {
		if err := w.ReplaceForce(context.Background(), uuid, string(md)); err != nil {
			return err
		}
		warnLag(stderr)
		return nil
	}

	degraded, err := w.Replace(context.Background(), uuid, string(md))
	var lossy *notesapp.ErrLossyRewrite
	if errors.As(err, &lossy) {
		return fmt.Errorf("%w.\n"+
			"       Rewriting the note from Markdown cannot carry that content.\n"+
			"       Pass -force to overwrite anyway", err)
	}
	if err != nil {
		return err
	}
	warnDegraded(stderr, degraded)
	warnLag(stderr)
	return nil
}

func rmNote(stderr io.Writer, path, uuid string) error {
	s, w, err := writer(path)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := w.Delete(context.Background(), uuid); err != nil {
		return err
	}
	warnLag(stderr)
	return nil
}

func decode(r io.Reader, stdout io.Writer) error {
	blob, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	n, err := notestore.Decode(blob)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, n.Markdown())
	return nil
}
