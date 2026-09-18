package main

import (
	"flag"
	"reflect"
	"strings"
	"testing"
)

// flagsLike mirrors the real flag set closely enough to test argument order:
// what matters is which flags take a value and which do not.
func flagsLike() *flag.FlagSet {
	fs := flag.NewFlagSet("notes", flag.ContinueOnError)
	fs.String("db", "", "")
	fs.String("folder", "", "")
	fs.String("server", "", "")
	fs.String("token", "", "")
	fs.Bool("deleted", false, "")
	fs.Bool("force", false, "")
	return fs
}

// The bug: "notes rm <uuid> -server URL" parsed -server as a positional and
// went looking for a local database instead. Writing the flag after the UUID is
// the natural way to type it, so both orders have to mean the same thing.
func TestFlagsAfterPositionalsAreStillFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want map[string]string
		rest []string
	}{
		{"flag after the uuid",
			[]string{"UUID", "-server", "http://mac:8437"},
			map[string]string{"server": "http://mac:8437"}, []string{"UUID"}},
		{"flag before the uuid",
			[]string{"-server", "http://mac:8437", "UUID"},
			map[string]string{"server": "http://mac:8437"}, []string{"UUID"}},
		{"several, interleaved",
			[]string{"-token", "t", "UUID", "-server", "http://mac:8437", "-force"},
			map[string]string{"server": "http://mac:8437", "token": "t", "force": "true"},
			[]string{"UUID"}},
		{"equals form needs no lookahead",
			[]string{"UUID", "-server=http://mac:8437"},
			map[string]string{"server": "http://mac:8437"}, []string{"UUID"}},
		{"double dash spelling",
			[]string{"UUID", "--server", "http://mac:8437"},
			map[string]string{"server": "http://mac:8437"}, []string{"UUID"}},
		// A boolean consumes no value, so the word after it stays positional.
		// It has to be checked with a flag still to come: if the boolean eats
		// the UUID, that flag ends up behind a positional and is lost again --
		// which is the whole bug, reintroduced one level down.
		{"boolean does not swallow the next word",
			[]string{"-deleted", "UUID", "-server", "http://mac:8437"},
			map[string]string{"deleted": "true", "server": "http://mac:8437"},
			[]string{"UUID"}},
		{"boolean at the front",
			[]string{"-force", "UUID"},
			map[string]string{"force": "true"}, []string{"UUID"}},
		{"boolean after the uuid",
			[]string{"UUID", "-deleted"},
			map[string]string{"deleted": "true"}, []string{"UUID"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := flagsLike()
			if err := fs.Parse(permute(fs, tc.args)); err != nil {
				t.Fatal(err)
			}
			for name, want := range tc.want {
				if got := fs.Lookup(name).Value.String(); got != want {
					t.Errorf("-%s = %q, want %q", name, got, want)
				}
			}
			if !reflect.DeepEqual(fs.Args(), tc.rest) {
				t.Errorf("positionals = %v, want %v", fs.Args(), tc.rest)
			}
		})
	}
}

// "--" is how a note whose text starts with a dash gets written, so everything
// after it has to survive as text rather than be read as flags.
func TestDoubleDashProtectsTextThatLooksLikeFlags(t *testing.T) {
	// "--" after a positional, so it has to be moved rather than merely left
	// where it fell: a "--" that stays put would terminate parsing early and
	// swallow the real arguments with it.
	fs := flagsLike()
	if err := fs.Parse(permute(fs, []string{"UUID", "--", "-force"})); err != nil {
		t.Fatal(err)
	}
	if fs.Lookup("force").Value.String() != "false" {
		t.Error("a word after -- was read as a flag")
	}
	if want := []string{"UUID", "-force"}; !reflect.DeepEqual(fs.Args(), want) {
		t.Errorf("positionals = %v, want %v", fs.Args(), want)
	}

	fs = flagsLike()
	if err := fs.Parse(permute(fs, []string{"-folder", "Work", "--", "-force", "-not-a-flag"})); err != nil {
		t.Fatal(err)
	}
	if got := fs.Lookup("folder").Value.String(); got != "Work" {
		t.Errorf("-folder = %q", got)
	}
	if fs.Lookup("force").Value.String() != "false" {
		t.Error("a word after -- was read as a flag")
	}
	if want := []string{"-force", "-not-a-flag"}; !reflect.DeepEqual(fs.Args(), want) {
		t.Errorf("positionals = %v, want %v", fs.Args(), want)
	}
}

// An unknown flag must still be an error the user sees, not something quietly
// reordered into a positional.
func TestUnknownFlagIsStillAnError(t *testing.T) {
	fs := flagsLike()
	fs.SetOutput(&strings.Builder{})
	if err := fs.Parse(permute(fs, []string{"UUID", "-nonesuch", "x"})); err == nil {
		t.Fatal("an unknown flag was accepted")
	}
}

// Rearranging arguments must not lose any.
func TestPermuteKeepsEveryArgument(t *testing.T) {
	for _, args := range [][]string{
		{}, {"UUID"}, {"-force"}, {"-server"}, // trailing flag with no value
		{"a", "-folder", "F", "b", "-deleted", "c"},
		{"-", "x"}, // a bare "-" is not a flag
	} {
		fs := flagsLike()
		got := permute(fs, args)
		count := map[string]int{}
		for _, a := range args {
			count[a]++
		}
		for _, a := range got {
			count[a]--
		}
		// permute may add a "--" separator of its own; nothing else may change.
		delete(count, "--")
		for a, n := range count {
			if n != 0 {
				t.Errorf("permute(%v) = %v: %q changed count by %d", args, got, a, -n)
			}
		}
	}
}
