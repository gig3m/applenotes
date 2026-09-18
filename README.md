# applenotes

Read and write Apple Notes from Linux, over Tailscale, using an old Mac as the bridge.

If you keep a Mac behind your firewall and you're willing to disable SIP on it,
this gives your Linux boxes real access to your iCloud Notes — the ones on your
iPhone — with formatting and hyperlinks intact.

**This requires disabling System Integrity Protection on the bridge Mac.** That
is a deliberate trade, the same one BlueBubbles asks for. See [Security](#security).

## Why this exists

Apple ships no Notes API. Unlike Reminders and Calendar, which have EventKit,
Notes has no framework at all — the only supported surface is AppleScript.

And **AppleScript silently destroys hyperlinks.** Ask Notes for a note's body
and a link comes back as underlined text with the URL gone:

```
stored in the note:   <a href="https://ref.ly/Heb13.17;nkjv">Hebrews 13:17</a>
AppleScript returns:  <u>Hebrews 13:17</u>
```

Read a note that way, edit it, write it back, and every link in it is gone —
and iCloud will faithfully sync that loss to all your devices. Tools that
round-trip notes through `osascript` have this bug whether they know it or not.

So this project splits the two directions:

| | mechanism | lossy? |
|---|---|---|
| **read** | `NoteStore.sqlite` → gzip → protobuf | no |
| **write** | Apple Events → Notes.app | no |

Writing HTML through Notes.app *does* preserve `<a href>`; only reading is
lossy. Verified by writing a note with links and checking the stored protobuf
rather than trusting what AppleScript read back.

## Facts worth knowing

- Note bodies live in `ZICNOTEDATA.ZDATA`: gzip, wrapping a protobuf of one
  flat text string plus styled runs over it. Markdown conversion is a run-walk,
  not HTML parsing.
- `AttributeRun.length` counts **UTF-16 code units**, not bytes or runes. Get
  this wrong and every note containing an emoji misaligns.
- The cross-device note ID is `ZICCLOUDSYNCINGOBJECT.ZIDENTIFIER`, a UUID.
  The `id` AppleScript hands you is `x-coredata://…/ICNote/p170`, whose last
  component is a **local Core Data row key** — it will not resolve on your
  phone. Build `applenotes:note/<ZIDENTIFIER>` deep links from SQLite.
- Setting both the `name` property and a title line in `body` gives you the
  title twice. Notes derives the title from the first line.
- `<h1>`/`<h2>` are lowered to bold 24px/18px spans on write.
- Adjacent `<ul>` and `<ol>` merge into one list. Separate them with a
  `<div><br></div>`.
- A heading created through HTML is stored as bold text at an enlarged point
  size with no paragraph style, so it is recovered by size on read (24pt → `#`,
  18pt → `##`). Text you manually bolded and enlarged will therefore read back
  as a heading.
- Locked (password-protected) notes are listed with their metadata, but
  their bodies are encrypted and are not readable here.

## Security

The bridge Mac needs, and this project's installer grants:

- **Automation** (`kTCCServiceAppleEvents`) over Notes.app — to write notes.
- **Full Disk Access** (`kTCCServiceSystemPolicyAllFiles`) — to read
  `NoteStore.sqlite`.

Both are written directly into TCC's database, which is only possible with SIP
disabled. Understand what that means: SIP off means any root process on that Mac
can rewrite those same permissions, and the machine holds your iCloud data in
plaintext. Run it behind a firewall, reachable only over Tailscale, on a machine
you don't use for anything else. `uninstall.sh` removes every grant it made.

## Status

The read path is complete and validated against a real library.

- [x] protobuf decode of note bodies
- [x] Markdown rendering (headings, lists, checklists, emphasis, links)
- [x] SQLite index reader (titles, folders, UUIDs, timestamps)
- [x] `notes` CLI: list, folders, show, decode
- [x] write path via Apple Events (`new`, `append`, `replace`, `rm`)
- [x] `notesd` HTTP+JSON daemon
- [ ] LaunchAgent and installer
- [ ] installer / TCC grants
- [ ] Linux TUI and Omarchy bar client

## Usage

```
notes list [-folder NAME] [-deleted]   list notes, newest first
notes folders                          list folders
notes show <uuid>                      print one note as Markdown
notes new [-folder NAME]               create a note from Markdown on stdin
notes append <uuid>                    append Markdown from stdin to a note
notes replace <uuid>                   overwrite a note with Markdown from stdin
notes rm <uuid>                        move a note to Recently Deleted
notes decode                           decode a raw ZICNOTEDATA blob on stdin
```

### Writing

Writes go through Apple Events to Notes.app, never through SQLite — the
database handle is opened read-only and Notes owns its own file. This needs
Automation access to Notes; see [Security](#security).

Markdown round-trips: headings, bullets, numbered lists, bold, italic,
bold-italic, strikethrough, inline code and **links with their URLs intact**
survive a write-then-read cycle unchanged, verified stable across three passes.

What does not survive, because Notes has no way to express it through HTML:

| Markdown | Becomes |
|---|---|
| `> quote` | literal `> text` — block quotes are not encoded at all, so the marker is kept as characters rather than losing the line |
| `- [ ]` / `- [x]` | a bullet prefixed `☐`/`☑`; real checklists cannot be created |
| fenced code | monospaced lines, not a block |
| `###` and deeper | clamped to `##` — h3 carries no point size, so Notes stores it as plain bold and it cannot be recovered |
| an unrecognised link scheme | text, not a live link — only http, https, mailto, tel, file, message and applenotes are linked |
| nested lists | flattened to one level |

### Writes are not immediately visible to reads

**Notes.app holds changes in memory and writes them to `NoteStore.sqlite` on its
own schedule.** Measured on macOS 15: a delete issued through Apple Events was
still absent from the database 60 seconds later, and only landed when Notes.app
quit. In between, the two disagreed in *both* directions — Notes.app no longer
had the note, while the database still listed it as live.

This is the single most important thing to know before building on this. A write
is not observable through the read path for an unbounded period, so:

- `notes new` prints the new note's UUID, but `notes show <uuid>` may not find
  it yet, and `notes list` may keep showing a note you just deleted.
- Anything that needs to confirm a write must poll for it. Read-after-write does
  not work.
- A file watcher on `NoteStore.sqlite` will not fire promptly after your own
  write, so it cannot be used to confirm one — only to notice changes made on
  other devices, once Notes gets around to persisting them.

There is no non-destructive way to append. Notes offers no append verb, so the
note must be rewritten whole, and both routes to its existing body lose
something: reading through AppleScript drops every hyperlink, and rewriting from
Markdown drops anything Markdown cannot express.

So `append` reads the body from SQLite, which keeps links, and **refuses**
rather than damaging a note whose content a rewrite would lose outright —
attachments and checklists. `replace` refuses on the same grounds; pass
`-force` to overwrite anyway. Edit those notes in Notes.app instead.

Formatting that merely flattens — indentation, block quotes, subheadings,
monospaced paragraphs, alignment, underlining, superscript — does **not** block
a write. Treating it as fatal would be worse than the problem: underlining
rides along with hyperlinks in real notes (a quarter of the link runs in a real
library carry it), so refusing on it would make most notes containing a link
unwritable.

Leading and repeated spaces are written as non-breaking spaces, because a plain
space is collapsed away by HTML. Indentation and the double space after a full
stop therefore survive a round trip, at the cost of the exact character: what
comes back is U+00A0, not U+0020.

Two caveats apply even when it succeeds: the read comes from the database, so an
edit still buffered in Notes.app is not merely missed but **overwritten**; and
Markdown metacharacters in the existing prose are re-escaped on the way through.

`show` takes the `ZIDENTIFIER` UUID that `list` prints. Notes in Recently
Deleted are hidden unless you ask for them, by any of the three routes into the
trash: the note's own deletion flag, living in the trash folder, or sitting in
a folder that is itself marked for deletion. Password-protected notes are
listed and flagged but their bodies are not readable.

`-folder` matches by name or UUID and does not descend into subfolders.

Reads are strictly read-only: the database is opened `mode=ro`, and nothing in
this tool modifies a note, the database, or iCloud. One caveat, because SQLite
requires it: opening a WAL-mode database creates its `-shm` and `-wal` sidecar
files if they are absent. Against a live Notes database both already exist and
nothing is created. Against a *copy*, SQLite will create empty sidecars next to
it — so copy `NoteStore.sqlite-wal` alongside the database, or you will also be
reading a stale snapshot missing everything Notes has not yet checkpointed.
(`-shm` need not be copied; SQLite rebuilds it from the `-wal`.)

## notesd

```
notesd [-addr 127.0.0.1:8437] [-token ~/.config/applenotes/token] [-db PATH]
```

Generates a bearer token on first run, 0600, and refuses to start if the file
is group- or world-readable. Every route requires it, including `/v1/healthz`:
on a tailnet there is no other perimeter.

| | |
|---|---|
| `GET /v1/folders` | folders, with the trash flagged |
| `GET /v1/notes?folder=&deleted=` | note metadata, newest first |
| `GET /v1/notes/{uuid}` | one note as Markdown, plus `degrades`/`destroys` |
| `POST /v1/notes` | `{markdown, folder}` |
| `PUT /v1/notes/{uuid}` | `{markdown, force}` |
| `POST /v1/notes/{uuid}/append` | `{markdown}` |
| `DELETE /v1/notes/{uuid}` | to Recently Deleted |

Writes answer **202 Accepted**, never 200. Notes.app persists on its own
schedule, so claiming the change is durable would be a lie — and a read straight
after a write may not show it. A rewrite that would destroy content answers
**409** naming what would be lost; retry with `force` to overwrite anyway.

Bind to loopback and reach it over Tailscale. Binding to a routable address logs
a warning, because the token is then the only thing between the network and
every note on the Mac.

## Prior art

- [threeplanetssoftware/apple_cloud_notes_parser](https://github.com/threeplanetssoftware/apple_cloud_notes_parser) — the reverse-engineered `notestore.proto` this decoder follows.
- [BlueBubbles](https://bluebubbles.app) — the same bargain, for iMessage.

## License

MIT
