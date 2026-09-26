package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func placing(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(switchboard.PlaceEnv, dir)
	return dir
}

func fed(t *testing.T, in io.Reader, argv ...string) (int, string, string) {
	t.Helper()
	var out, errs strings.Builder
	return run(t.Context(), argv, in, &out, &errs), out.String(), errs.String()
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	read, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range read {
		names = append(names, entry.Name())
	}
	return names
}

func holding(t *testing.T, path string) string {
	t.Helper()
	held, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(held)
}

func TestAPlacedFileIsWhatWasFedAndNothingElseIsLeftBeside(t *testing.T) {
	dir := placing(t)
	at := filepath.Join(dir, "ocel.yml")

	if code, _, errs := fed(t, strings.NewReader("http:\n  routers: {}\n"), "place", at); code != 0 {
		t.Fatalf("place = %d: %s", code, errs)
	}
	if got := holding(t, at); got != "http:\n  routers: {}\n" {
		t.Errorf("%s holds %q, want what place was fed", at, got)
	}
	if got := entries(t, dir); !slices.Equal(got, []string{"ocel.yml"}) {
		t.Errorf("the directory holds %q after the place, want ocel.yml alone: anything left beside it is a file the proxy may read", got)
	}
	held, err := os.Stat(at)
	if err != nil {
		t.Fatal(err)
	}
	if mode := held.Mode().Perm(); mode != 0o644 {
		t.Errorf("%s is mode %v, want 0644: the proxy that reads it may run as a user other than the one that placed it", at, mode)
	}
}

func TestAReaderOfAFileBeingPlacedSeesTheOldOneWholeUntilTheNewOneIsWhole(t *testing.T) {
	dir := placing(t)
	at := filepath.Join(dir, "ocel.caddy")
	if err := os.WriteFile(at, []byte("the old routes\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	reading, writing := io.Pipe()
	exited := make(chan int, 1)
	go func() {
		code, _, _ := fed(t, reading, "place", at)
		exited <- code
	}()
	if _, err := io.WriteString(writing, "the first half of "); err != nil {
		t.Fatal(err)
	}

	var staged []string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if staged = slices.DeleteFunc(entries(t, dir), func(name string) bool { return name == "ocel.caddy" }); len(staged) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(staged) != 1 {
		t.Fatalf("a place half fed stages %q beside the file, want the one file it writes into", staged)
	}
	name := staged[0]
	if !strings.HasPrefix(name, ".ocel.") || filepath.Ext(name) != ".tmp" {
		t.Errorf("a place stages into %q, want .ocel.<random>.tmp", name)
	}
	for _, read := range []string{".yml", ".yaml", ".toml", ".caddy"} {
		if strings.HasSuffix(name, read) {
			t.Errorf("a place stages into %q, which a proxy reading every %s file in the directory parses half written", name, read)
		}
	}
	if got := holding(t, at); got != "the old routes\n" {
		t.Errorf("%s reads %q while the place is half fed, want the old file whole", at, got)
	}

	if _, err := io.WriteString(writing, "the new routes\n"); err != nil {
		t.Fatal(err)
	}
	writing.Close()
	if code := <-exited; code != 0 {
		t.Fatalf("place = %d", code)
	}
	if got := holding(t, at); got != "the first half of the new routes\n" {
		t.Errorf("%s reads %q once the place returns, want the new file whole", at, got)
	}
	if got := entries(t, dir); !slices.Equal(got, []string{"ocel.caddy"}) {
		t.Errorf("the directory holds %q after the place, want ocel.caddy alone", got)
	}
}

type severed struct{ said bool }

func (s *severed) Read(into []byte) (int, error) {
	if s.said {
		return 0, errors.New("the connection dropped")
	}
	s.said = true
	return copy(into, "half a file"), nil
}

func TestAPlaceWhoseFeedIsCutShortLeavesTheOldFileAndNothingBesideIt(t *testing.T) {
	dir := placing(t)
	at := filepath.Join(dir, "ocel.yml")
	if err := os.WriteFile(at, []byte("the old routes\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, errs := fed(t, &severed{}, "place", at)
	if code != exitRefused {
		t.Errorf("a place whose feed dropped = %d, want %d", code, exitRefused)
	}
	if !strings.Contains(errs, "the connection dropped") {
		t.Errorf("a place whose feed dropped said %q, which never says why", errs)
	}
	if got := holding(t, at); got != "the old routes\n" {
		t.Errorf("%s reads %q after a place whose feed dropped, want the old file untouched", at, got)
	}
	if got := entries(t, dir); !slices.Equal(got, []string{"ocel.yml"}) {
		t.Errorf("the directory holds %q after a place whose feed dropped, want ocel.yml alone", got)
	}
}

func TestUnplaceRemovesThePlacedFileAndAFileAlreadyGoneIsNoFailure(t *testing.T) {
	dir := placing(t)
	at := filepath.Join(dir, "ocel.yml")
	if err := os.WriteFile(at, []byte("the routes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	beside := filepath.Join(dir, "coolify.yaml")
	if err := os.WriteFile(beside, []byte("their routes\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if code, _, errs := ran(t, "unplace", at); code != 0 {
			t.Fatalf("unplace = %d: %s", code, errs)
		}
	}
	if got := entries(t, dir); !slices.Equal(got, []string{"coolify.yaml"}) {
		t.Errorf("the directory holds %q after unplace, want only what ocel never placed", got)
	}
}

func TestPlacedSaysTheSumOfThePlacedFileOrNothingWhenItIsGone(t *testing.T) {
	dir := placing(t)
	at := filepath.Join(dir, "ocel.yml")

	if code, out, errs := ran(t, "placed", at); code != 0 || out != "" {
		t.Errorf("placed of a file not there = %d, %q, %q, want nothing said", code, out, errs)
	}
	if err := os.WriteFile(at, []byte("the routes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("the routes\n"))
	if code, out, errs := ran(t, "placed", at); code != 0 || out != hex.EncodeToString(sum[:])+"\n" {
		t.Errorf("placed = %d, %q, %q, want the sha256 of what the file holds", code, out, errs)
	}
}

func TestEveryPathOutsideTheMountedDirectoryIsRefusedAndNothingIsTouched(t *testing.T) {
	dir := placing(t)
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.yml")
	if err := os.WriteFile(victim, []byte("theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}

	for what, path := range map[string]string{
		"another directory":       victim,
		"an escape through ..":    filepath.Join(dir, "..", filepath.Base(outside), "victim.yml"),
		"an unclean path":         dir + "//ocel.yml",
		"a relative path":         "ocel.yml",
		"the directory itself":    dir,
		"a directory beneath it":  filepath.Join(dir, "nested", "ocel.yml"),
		"a name a place stages":   filepath.Join(dir, ".ocel.1234.tmp"),
		"the directory's parent":  filepath.Dir(dir),
		"nothing but a separator": "/",
	} {
		for _, verb := range []string{"place", "unplace", "placed"} {
			code, out, errs := fed(t, strings.NewReader("ocel's routes\n"), verb, path)
			if code != exitRefused || out != "" {
				t.Errorf("%s of %s (%s) = %d, %q, want refused with nothing said", verb, what, path, code, out)
			}
			if !strings.Contains(errs, dir) {
				t.Errorf("%s of %s refused with %q, which never names %s, the one directory it places in", verb, what, errs, dir)
			}
		}
	}
	if got := holding(t, victim); got != "theirs\n" {
		t.Errorf("%s reads %q, want it untouched", victim, got)
	}
	if got := entries(t, dir); !slices.Equal(got, []string{"nested"}) {
		t.Errorf("the directory holds %q after every refusal, want nothing placed", got)
	}
}

func TestASwitchboardWithNoDirectoryMountedPlacesNothing(t *testing.T) {
	t.Setenv(switchboard.PlaceEnv, "")
	at := filepath.Join(t.TempDir(), "ocel.yml")

	for _, verb := range []string{"place", "unplace", "placed"} {
		if code, _, errs := fed(t, strings.NewReader("ocel's routes\n"), verb, at); code != exitRefused || !strings.Contains(errs, switchboard.PlaceEnv) {
			t.Errorf("%s with no directory mounted = %d, %q, want refused naming %s", verb, code, errs, switchboard.PlaceEnv)
		}
	}
	if _, err := os.Stat(at); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s was placed by a switchboard with no directory mounted: %v", at, err)
	}
	if code, _, _ := ran(t, "place"); code != exitRefused {
		t.Errorf("place of nothing = %d, want the usage refusal", code)
	}
}
