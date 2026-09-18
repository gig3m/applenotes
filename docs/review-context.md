# Review context

Everything here was established by measurement against a real macOS 15 machine
and a real 52-note library, or by fuzzing. It is the shared premise for code
review: reviewers should not spend time re-deriving it, and should treat a
contradiction of it as a finding about the code, not about this document.

## How Notes stores a note

- A note body is `ZICNOTEDATA.ZDATA`: gzip wrapping a protobuf of one flat text
  string plus styled runs over it. Markdown conversion is a run-walk.
- `AttributeRun.length` counts **UTF-16 code units**, not bytes or runes.
- `font_weight` is a **style enum**, not a weight: 1 bold, 2 italic, 3 both.
  `Font.font_hints` is never populated.
- A paragraph's terminating newline is its **own run, carrying that paragraph's
  style**. Such a run attaches to the line it terminates, not the next one.
- A heading created through HTML has **no paragraph style**: it is bold text at
  an enlarged point size. h1 is 24pt, h2 is 18pt, **h3 carries no point size at
  all**, so `###` and deeper cannot round-trip.
- The cross-device note id is `ZICCLOUDSYNCINGOBJECT.ZIDENTIFIER`, a UUID. The
  `x-coredata://…/ICNote/pN` id AppleScript uses embeds a **local row number**
  and will not resolve on another machine. `Z_METADATA.Z_UUID` is the store id.
- A note reaches Recently Deleted by three independent routes: its own
  `ZMARKEDFORDELETION`, living in the trash folder, or its folder being marked
  for deletion. All three occur.
- Underlining rides along with hyperlinks: **21 of 82 link runs** in a real
  library carry `underlined=1`. It cannot be treated as a reason to refuse.

## How Notes behaves

- **AppleScript reading a note body silently drops every hyperlink href**,
  returning `<a href=…>` as `<u>`. Writing one preserves it. This is why reads
  go to SQLite and only writes go through Apple Events.
- **Notes.app buffers writes in memory.** A delete was still absent from the
  database 60 seconds later and only landed when Notes.app quit. Read-after-write
  does not work, and a file watcher cannot confirm your own write. Any guard that
  reads SQLite is evaluating a possibly-stale snapshot.
- `osascript` does not interpret argv after `-`: an argument of
  `-e 'do shell script "touch /tmp/pwn"'` came back as data and executed nothing.
  This is what makes fixed-script + argv safe, and it is load-bearing.
- Notes cannot create a checklist or a block quote through HTML, and merges
  adjacent `<ul>`/`<ol>` unless something separates them.
- Setting both the `name` property and a title line yields the title twice;
  Notes takes the title from the body's first line.

## HTML that Notes renders

- A plain space is **collapsed**, so leading, trailing and repeated spaces must
  be `&#160;`. A numeric reference to U+0020 does not help: it is still a space
  by the time layout runs.
- Character references are resolved after tokenizing, so `&lt;script&gt;` in
  note text renders as visible characters, never a tag.

## The installer, as run

Verified end to end on a real SIP-disabled Mac (2026-09-18), which settled two
things that could only be answered there:

- A TCC grant with `auth_reason=3` and a **NULL `csreq`** is accepted by macOS
  15. Path-keyed grants work without a code-signing requirement.
- `sqlite3 -cmd ".param set ?1 ..."` binds correctly against the real TCC
  database, so a path containing an apostrophe is safe.

Both grants took effect: Full Disk Access read the library, Automation wrote a
note. Write the user's TCC database **without** sudo — as root it leaves
root-owned `-wal` files that can stop the account's own `tccd` writing at all.

## Recurring failure modes in this project

Check each of these before committing. Every one has caught a real bug here, and
the first has caught four.

1. **Does the test fail if you delete the fix?** Four tests in this repo passed
   with the mechanism they existed to protect removed. A round trip that
   normalises away the thing under test cannot see it — assert on the artifact.
2. **Does the test assert the invariant, or two strings you happened to pick?**
   An assertion on `"[a["` missed the same bug spelled `")[["`.
3. **Does the fixture actually have the property the test is named for?** A
   "merely degraded note passes" test used a note with no degradation.
4. **Did you verify a write with a reader you have proven lossy?** Checking an
   AppleScript write by reading it back through AppleScript "proved" that links
   were being dropped on write. They were not; the reader was dropping them.
5. **Does the guard fail open?** Twice: a body that could not be read skipped
   the check, and attachment detection keyed only on metadata the decoder is
   documented to skip when malformed.
6. **Did you fix every branch, or the one in front of you?** The same whitespace
   defect was fixed in one block branch, then five, then the sixth.
7. **Is the flag actually wired?** `-force` was registered, documented and
   threaded in as a parameter that was never read. Go does not flag that.
8. **Is the error message true?** A warning said "No text is lost" while the
   path it described was reflowing code blocks.
