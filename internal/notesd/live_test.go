package notesd

import (
	"context"
	"errors"
	"testing"

	"github.com/gig3m/applenotes/internal/applescript"
	"github.com/gig3m/applenotes/internal/notestore"
)

func runner(out string, err error) applescript.RunnerFunc {
	return func(context.Context, string, ...string) (string, error) { return out, err }
}

func metas(ids ...int64) []notestore.NoteMeta {
	var out []notestore.NoteMeta
	for _, id := range ids {
		out = append(out, notestore.NoteMeta{ID: id})
	}
	return out
}

func ids(notes []notestore.NoteMeta) []int64 {
	out := []int64{}
	for _, n := range notes {
		out = append(out, n.ID)
	}
	return out
}

const reply = "x-coredata://STORE/ICNote/p120, x-coredata://STORE/ICNote/p133"

// The bug: a note deleted in Notes.app keeps its database row, pointing at its
// old folder with no deletion flag, for far longer than anyone will wait. It
// therefore lists as ordinary, and opening it and typing fails at save with
// "can't modify a note in Recently Deleted" -- for a note the user had no
// reason to think was deleted.
func TestNotesTheAppNoLongerHasAreDropped(t *testing.T) {
	l := newLiveSet(runner(reply, nil))
	got := ids(l.filter(context.Background(), metas(120, 133, 237)))
	if len(got) != 2 || got[0] != 120 || got[1] != 133 {
		t.Errorf("got %v, want [120 133] -- 237 is gone from Notes.app", got)
	}
}

// Fails open. Showing a stale note is a far smaller fault than showing none,
// so every way of not getting an answer leaves the database's answer alone.
func TestAFailedCheckLeavesTheListAlone(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    applescript.RunnerFunc
	}{
		{"Notes cannot be asked", runner("", errors.New("not running"))},
		{"the reply is empty", runner("", nil)},
		{"the reply has no ids in it", runner("something else entirely", nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newLiveSet(tc.r)
			got := ids(l.filter(context.Background(), metas(1, 2, 3)))
			if len(got) != 3 {
				t.Errorf("got %v, want every note kept", got)
			}
		})
	}
}

// A momentary failure must not make deleted notes reappear: once an answer is
// known, it is kept until a better one arrives.
func TestAPreviousAnswerSurvivesAFailure(t *testing.T) {
	calls := 0
	l := newLiveSet(applescript.RunnerFunc(func(context.Context, string, ...string) (string, error) {
		calls++
		if calls == 1 {
			return reply, nil
		}
		return "", errors.New("Notes stopped responding")
	}))
	l.filter(context.Background(), metas(120))
	l.fetched = l.fetched.Add(-liveTTL * 2) // force a refetch
	got := ids(l.filter(context.Background(), metas(120, 237)))
	if len(got) != 1 || got[0] != 120 {
		t.Errorf("got %v, want the last good answer kept", got)
	}
}

// One Apple Event per listing is affordable; one per request is not, and a list
// that is scrolled or polled would fire many.
func TestTheAnswerIsReused(t *testing.T) {
	calls := 0
	l := newLiveSet(applescript.RunnerFunc(func(context.Context, string, ...string) (string, error) {
		calls++
		return reply, nil
	}))
	for i := 0; i < 5; i++ {
		l.filter(context.Background(), metas(120))
	}
	if calls != 1 {
		t.Errorf("asked Notes.app %d times, want 1 within the cache window", calls)
	}
}
