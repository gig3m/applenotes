package notesd

import (
	"context"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/gig3m/applenotes/internal/applescript"
	"github.com/gig3m/applenotes/internal/notestore"
)

// The database lags Notes.app, and deletes lag worst.
//
// A note deleted in Notes.app -- on the Mac, or on a phone that syncs to it --
// keeps its row for a long time, still pointing at its old folder with no
// deletion flag set. Reading the database alone therefore lists notes that are
// gone, and a client cannot tell: it opens one, edits it, and the write fails
// with "can't modify a note in Recently Deleted" for a note the user believes
// is ordinary.
//
// Asking Notes.app which notes it actually has costs one Apple Event and about
// 200ms for a whole library, because the ids come back in a single reply. That
// is cheap enough to do on every listing, and it is only ids -- no note content
// crosses AppleScript, so the reason reads come from SQLite in the first place
// still holds.

const liveScript = `on run argv
	tell application "Notes"
		set out to {}
		repeat with f in folders of account "iCloud"
			if name of f is not "Recently Deleted" then
				set out to out & (id of every note of f)
			end if
		end repeat
		return out
	end tell
end run`

// coreDataID pulls the row id out of "x-coredata://<store>/ICNote/p123". The
// trailing number is the SQLite primary key, which is what makes this cheap:
// the reply can be matched against rows without a second lookup.
var coreDataID = regexp.MustCompile(`/ICNote/p(\d+)`)

type liveSet struct {
	mu      sync.Mutex
	ids     map[int64]bool
	fetched time.Time
	runner  applescript.Runner
}

// liveTTL is how long a reply is reused. Long enough that scrolling a list does
// not fire an Apple Event per request, short enough that deleting a note in
// Notes.app is reflected while the user is still looking at the window.
const liveTTL = 5 * time.Second

func newLiveSet(r applescript.Runner) *liveSet { return &liveSet{runner: r} }

// filter drops notes Notes.app no longer has.
//
// Fails open. If Notes.app cannot be asked -- it is not running, the Mac is
// busy, automation is refused -- the database's answer is returned unchanged,
// because showing a stale note is a far smaller fault than showing none.
func (l *liveSet) filter(ctx context.Context, notes []notestore.NoteMeta) []notestore.NoteMeta {
	ids := l.get(ctx)
	if ids == nil {
		return notes
	}
	out := notes[:0:0]
	for _, n := range notes {
		// A trashed note is already excluded by the caller when it wants it
		// excluded; this only removes rows Notes.app does not have at all.
		if ids[n.ID] {
			out = append(out, n)
		}
	}
	return out
}

func (l *liveSet) get(ctx context.Context) map[int64]bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ids != nil && time.Since(l.fetched) < liveTTL {
		return l.ids
	}
	out, err := l.runner.Run(ctx, liveScript)
	if err != nil {
		// Keep serving the previous answer rather than falling back to the
		// database mid-session: a momentary failure should not make deleted
		// notes reappear.
		return l.ids
	}
	ids := map[int64]bool{}
	for _, m := range coreDataID.FindAllStringSubmatch(out, -1) {
		if pk, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			ids[pk] = true
		}
	}
	// An empty reply means a library with no notes, which is possible but far
	// more likely to be a parse or permissions problem. Refusing to believe it
	// costs a stale listing; believing it empties the window.
	if len(ids) == 0 {
		return l.ids
	}
	l.ids, l.fetched = ids, time.Now()
	return l.ids
}
