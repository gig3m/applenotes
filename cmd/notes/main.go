// Command notes reads Apple Notes from the local NoteStore database.
//
// It is read-only: nothing here writes to Notes or to iCloud.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/gig3m/applenotes/internal/notestore"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]

	// Flags are parsed per subcommand rather than globally: the stdlib flag
	// package stops at the first non-flag argument, so a global FlagSet would
	// silently ignore everything after the command name.
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	fs.Usage = usage
	dbPath := fs.String("db", "", "path to NoteStore.sqlite (default: the current user's)")
	var folder *string
	var deleted *bool
	if cmd == "list" {
		folder = fs.String("folder", "", "limit to a folder, by name or UUID")
		deleted = fs.Bool("deleted", false, "include notes in Recently Deleted")
	}
	if err := fs.Parse(os.Args[2:]); err != nil {
		os.Exit(2)
	}

	var err error
	switch cmd {
	case "list":
		err = list(*dbPath, *folder, *deleted)
	case "folders":
		err = folders(*dbPath)
	case "show":
		if fs.NArg() < 1 {
			err = fmt.Errorf("show needs a note UUID")
		} else {
			err = show(*dbPath, fs.Arg(0))
		}
	case "decode":
		err = decode(os.Stdin)
	case "-h", "--help", "help":
		usage()
		return
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "notes:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: notes <command> [flags]

commands:
  list [-folder NAME] [-deleted]   list notes, newest first
  folders                          list folders
  show <uuid>                      print one note as Markdown
  decode                           decode a raw ZICNOTEDATA blob on stdin

common flags:
  -db PATH   path to NoteStore.sqlite (default: the current user's)
`)
}

func open(path string) (*notestore.Store, error) { return notestore.Open(path) }

func list(path, folder string, deleted bool) error {
	s, err := open(path)
	if err != nil {
		return err
	}
	defer s.Close()

	notes, err := s.Notes(notestore.ListOptions{IncludeDeleted: deleted, Folder: folder})
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "MODIFIED\tFOLDER\tUUID\tTITLE")
	for _, n := range notes {
		title := n.Title
		if n.Locked {
			title += "  [locked]"
		}
		if n.Deleted {
			title += "  [deleted]"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			n.Modified.Local().Format("2006-01-02 15:04"), n.FolderName, n.UUID, title)
	}
	return w.Flush()
}

func folders(path string) error {
	s, err := open(path)
	if err != nil {
		return err
	}
	defer s.Close()

	fs, err := s.Folders()
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tUUID")
	for _, f := range fs {
		fmt.Fprintf(w, "%s\t%s\n", f.Name, f.UUID)
	}
	return w.Flush()
}

func show(path, uuid string) error {
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
	fmt.Println(body.Markdown())
	return nil
}

func decode(r io.Reader) error {
	blob, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	n, err := notestore.Decode(blob)
	if err != nil {
		return err
	}
	fmt.Println(n.Markdown())
	return nil
}
