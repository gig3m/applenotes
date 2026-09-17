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
- Locked notes are invisible to this, by design.

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

Early. The read path is implemented and validated against a real library.

- [x] protobuf decode of note bodies
- [x] Markdown rendering (headings, lists, checklists, emphasis, links)
- [ ] SQLite index reader (titles, folders, UUIDs, timestamps)
- [ ] write path via Apple Events
- [ ] `notesd` HTTP+JSON daemon and LaunchAgent
- [ ] installer / TCC grants
- [ ] Linux TUI and Omarchy bar client

## Prior art

- [threeplanetssoftware/apple_cloud_notes_parser](https://github.com/threeplanetssoftware/apple_cloud_notes_parser) — the reverse-engineered `notestore.proto` this decoder follows.
- [BlueBubbles](https://bluebubbles.app) — the same bargain, for iMessage.

## License

MIT
