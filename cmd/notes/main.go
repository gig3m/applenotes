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
	"runtime"
	"strings"
	"text/tabwriter"
	"unicode/utf16"

	"github.com/gig3m/applenotes/internal/notesapp"
	"github.com/gig3m/applenotes/internal/notestore"
)

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

// needsNotes reports whether a command reads or writes the note library, as
// opposed to working on its input.
func needsNotes(cmd string) bool {
	switch cmd {
	case "decode", "help", "-h", "--help":
		return false
	}
	return true
}

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
	// silently ignore everything after the command name. permute below deals
	// with the same rule biting again inside a subcommand.
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
	rawRuns := fs.Bool("raw", false, "decode: dump the attribute runs instead of Markdown")
	rest := args[1:]
	if permutable[cmd] {
		var err error
		if rest, err = permute(fs, rest); err != nil {
			fmt.Fprintf(stderr, "notes: %v\n\n", err)
			fmt.Fprint(stderr, usageText)
			return 2
		}
	}
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	// Looking for a local notes database on a machine that cannot have one is
	// never right, and the resulting stat error tells the user nothing about
	// what to do. Catch it before any command runs.
	// An explicit -db is always honoured: a copied database is a legitimate
	// thing to read on any machine.
	if *server == "" && *dbPath == "" && runtime.GOOS != "darwin" && needsNotes(cmd) {
		if cmd == "bar" {
			// The bar's contract is one JSON object per run, whatever happened.
			writeJSON(stdout, barOutput{
				Text:    "notes ?",
				Class:   "unreachable",
				Tooltip: "no notesd URL: set NOTESD_URL to the daemon on your Mac",
			})
			return 0
		}
		fmt.Fprint(stderr, `notes: no notesd URL, and this machine has no local notes database.

Point it at the daemon on your Mac, either way round:

  export NOTESD_URL=http://<mac-tailnet-ip>:8437
  notes `+cmd+`

  notes `+cmd+` -server http://<mac-tailnet-ip>:8437

The token is read from $NOTESD_TOKEN or ~/.config/applenotes/token.
`)
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
		if *server != "" {
			err = remoteNew(stdin, stdout, stderr, *server, *token, *folder)
			break
		}
		err = newNote(stdin, stdout, stderr, *dbPath, *folder)
	case "append":
		if fs.NArg() != 1 {
			return usageErr("append needs exactly one note UUID")
		}
		if *server != "" {
			err = remoteAppend(stdin, stderr, *server, *token, fs.Arg(0))
			break
		}
		err = appendNote(stdin, stderr, *dbPath, fs.Arg(0))
	case "replace":
		if fs.NArg() != 1 {
			return usageErr("replace needs exactly one note UUID")
		}
		if *server != "" {
			err = remoteReplace(stdin, stderr, *server, *token, fs.Arg(0), *force)
			break
		}
		err = replaceNote(stdin, stderr, *dbPath, fs.Arg(0), *force)
	case "rm":
		if fs.NArg() != 1 {
			return usageErr("rm needs exactly one note UUID")
		}
		if *server != "" {
			err = remoteRm(stderr, *server, *token, fs.Arg(0))
			break
		}
		err = rmNote(stderr, *dbPath, fs.Arg(0))
	case "edit":
		if fs.NArg() != 1 {
			return usageErr("edit needs exactly one note UUID")
		}
		err = editNote(stderr, *dbPath, *server, *token, fs.Arg(0), *force)
	case "capture":
		err = captureNote(stdin, stdout, stderr, *dbPath, *server, *token, *folder, strings.Join(fs.Args(), " "))
	case "search":
		if fs.NArg() == 0 {
			return usageErr("search needs something to look for")
		}
		// search does not permute, so a flag written after the query is part of
		// the query and will simply match nothing. Say so: silently searching
		// for "-folder Work" looks like the folder is empty.
		warnFlagInText(stderr, "search", fs)
		err = search(stdout, *dbPath, *server, *token, *folder, strings.Join(fs.Args(), " "), *deleted)
	case "bar":
		if fs.NArg() > 0 {
			return usageErr("bar takes no arguments")
		}
		err = bar(stdout, *dbPath, *server, *token, *folder)
	case "decode":
		if fs.NArg() > 0 {
			return usageErr("decode reads from stdin and takes no arguments")
		}
		err = decode(stdin, stdout, *rawRuns)
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
  search <text…>                   find notes containing a phrase, bodies too
  show <uuid>                      print one note as Markdown
  new [-folder NAME]               create a note from Markdown on stdin
  append <uuid>                    append Markdown from stdin to a note
  replace [-force] <uuid>          overwrite a note with Markdown from stdin
  edit [-force] <uuid>             open a note in an editor, write it back
  capture [text…]                  make a note from one line, or from stdin
  bar                              one line of JSON for a status bar
  rm <uuid>                        move a note to Recently Deleted
  decode [-raw]                    decode a raw ZICNOTEDATA blob on stdin

environment:
  NOTES_EDITOR   editor for 'edit'. Falls back to $VISUAL, then $EDITOR, then
                 a terminal editor on PATH. An $EDITOR that does not wait for
                 you to finish (a desktop launcher) is not used, since the edit
                 would be lost; set NOTES_EDITOR to override that.

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

func decode(r io.Reader, stdout io.Writer, raw bool) error {
	blob, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	n, err := notestore.Decode(blob)
	if err != nil {
		return err
	}
	if !raw {
		fmt.Fprintln(stdout, n.Markdown())
		return nil
	}
	// The run dump exists to answer "what did Notes actually store?" -- the
	// Markdown cannot say, because anything Markdown has no syntax for is
	// exactly what is missing from it. Conversion work is guesswork without it.
	fmt.Fprintf(stdout, "text: %d UTF-16 units, %d runs\n\n", utf16Len(n.Text), len(n.Runs))
	at := 0
	for i := range n.Runs {
		r := &n.Runs[i]
		fmt.Fprintf(stdout, "run %-3d len=%-5d %s\n", i, r.Length, runSummary(r))
		fmt.Fprintf(stdout, "        %q\n", utf16Slice(n.Text, at, r.Length))
		at += r.Length
	}
	return nil
}

func runSummary(r *notestore.AttributeRun) string {
	var f []string
	switch r.FontWeight {
	case 1:
		f = append(f, "bold")
	case 2:
		f = append(f, "italic")
	case 3:
		f = append(f, "bold+italic")
	}
	if r.Underlined {
		f = append(f, "underline")
	}
	if r.Strikethrough {
		f = append(f, "strike")
	}
	if r.Superscript > 0 {
		f = append(f, "superscript")
	} else if r.Superscript < 0 {
		f = append(f, "subscript")
	}
	if r.Link != "" {
		f = append(f, "link="+r.Link)
	}
	if r.FontName != "" {
		f = append(f, "font="+r.FontName)
	}
	if r.PointSize != 0 {
		f = append(f, fmt.Sprintf("pt=%g", r.PointSize))
	}
	if a := r.Attachment; a != nil {
		f = append(f, "attachment="+a.TypeUTI)
	}
	if ps := r.ParagraphStyle; ps != nil {
		f = append(f, "style="+styleName(ps.StyleType))
		if ps.IndentAmount != 0 {
			f = append(f, fmt.Sprintf("indent=%d", ps.IndentAmount))
		}
		if ps.Alignment != 0 {
			f = append(f, fmt.Sprintf("align=%d", ps.Alignment))
		}
		if ps.BlockQuote != 0 {
			f = append(f, fmt.Sprintf("quote=%d", ps.BlockQuote))
		}
		if ps.Checklist != nil {
			f = append(f, "checklist")
		}
	}
	if len(f) == 0 {
		return "plain"
	}
	return strings.Join(f, " ")
}

// styleName names the paragraph style constants Notes uses, so a dump reads as
// "bullet" rather than "104".
func styleName(t int) string {
	switch t {
	case -1:
		return "body"
	case 0:
		return "title"
	case 1:
		return "heading"
	case 2:
		return "subheading"
	case 3:
		return "monospaced"
	case 100:
		return "bullet"
	case 101:
		return "dashed"
	case 102:
		return "numbered"
	case 103:
		return "checklist"
	}
	return fmt.Sprintf("style(%d)", t)
}

func utf16Len(s string) int {
	return len(utf16.Encode([]rune(s)))
}

// utf16Slice takes a run's span out of the text. Run lengths are UTF-16 code
// units, so slicing by byte or rune silently misaligns on anything non-ASCII.
func utf16Slice(s string, at, n int) string {
	u := utf16.Encode([]rune(s))
	if at > len(u) {
		return ""
	}
	end := at + n
	if end > len(u) {
		end = len(u)
	}
	return string(utf16.Decode(u[at:end]))
}

// permute moves flags ahead of positional arguments, so that
//
//	notes rm <uuid> -server URL
//
// means what it looks like. The stdlib flag package stops at the first
// non-flag argument, so without this the -server above is parsed as another
// positional and quietly does nothing -- the command then goes looking for a
// local database on a machine that has none. Everything after a bare "--" is
// left alone, which is how a note whose text begins with "-" gets written.
// permutable lists the commands whose positional arguments are UUIDs, which
// can never look like a flag. Everything else keeps the stdlib rule that
// parsing stops at the first positional.
//
// This is an allow-list rather than a deny-list because the cost of being wrong
// is not symmetric. capture and search take free text, and reordering that text
// does not merely confuse the parse -- "notes capture fix the -server timeout"
// loses two words and sends the note to a daemon named "timeout". A command
// added later is better off with the surprising-but-harmless stdlib behaviour
// than with its arguments silently rewritten.
var permutable = map[string]bool{
	"show": true, "append": true, "replace": true, "rm": true, "edit": true,
	"list": true, "folders": true, "new": true, "bar": true,
}

// warnFlagInText reports a positional that names a real flag. For a free-text
// command that is not an error -- the text is the text -- but it is almost
// never what the user meant, and the result (no matches, or a note with a stray
// flag in it) gives no hint about why.
func warnFlagInText(stderr io.Writer, cmd string, fs *flag.FlagSet) {
	for _, a := range fs.Args() {
		name := strings.TrimLeft(a, "-")
		if name == a || name == "" {
			continue
		}
		if i := strings.IndexByte(name, '='); i >= 0 {
			name = name[:i]
		}
		if fs.Lookup(name) == nil {
			continue
		}
		fmt.Fprintf(stderr,
			"notes: %q here is part of the text, not a flag, because %s takes\n"+
				"       everything after the first word as its argument.\n"+
				"       Write flags first: notes %s %s …\n",
			a, cmd, cmd, a)
		return
	}
}

func permute(fs *flag.FlagSet, args []string) ([]string, error) {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(a) < 2 || a[0] != '-' {
			positional = append(positional, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.ContainsRune(name, '=') {
			continue // -flag=value carries its own value
		}
		f := fs.Lookup(name)
		if f == nil {
			continue // unknown: let Parse report it rather than guess its arity
		}
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			continue // -force takes no value; the next word is positional
		}
		if i+1 >= len(args) {
			// A value-taking flag with nothing after it. Reported here rather
			// than left to Parse: moving it to the front would put the "--"
			// separator next to it, and it would swallow that as its value.
			return nil, fmt.Errorf("flag needs an argument: %s", a)
		}
		i++
		flags = append(flags, args[i])
	}
	// The "--" is re-emitted rather than dropped: without it Parse would read
	// the moved positionals as flags again, and a note whose text starts with a
	// dash would fail instead of being written.
	if len(positional) == 0 {
		return flags, nil
	}
	return append(append(flags, "--"), positional...), nil
}
