package browser

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

const pageURL = "https://console.test/cloud/jobs/job_1?tab=input&x=1"

type fallback struct{ opened []string }

func (f *fallback) open(url string) error {
	f.opened = append(f.opened, url)
	return nil
}

func recorder(t *testing.T, name string) (command, argv string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the recording browser is a shell script")
	}
	dir := t.TempDir()
	argv = filepath.Join(dir, "argv")
	command = filepath.Join(dir, name)
	script := "#!/bin/sh\nfor arg in \"$@\"; do printf '%s\\n' \"$arg\" >> '" + argv + "'; done\n"
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return command, argv
}

func received(t *testing.T, argv string) []string {
	t.Helper()
	raw, err := os.ReadFile(argv)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

func TestABrowserCommandWithoutAPlaceholderGetsTheURLAsItsLastArgument(t *testing.T) {
	command, argv := recorder(t, "browser")
	var fell fallback

	if err := open(pageURL, command+" --new-window", fell.open); err != nil {
		t.Fatal(err)
	}
	if got, want := received(t, argv), []string{"--new-window", pageURL}; !slices.Equal(got, want) {
		t.Fatalf("the browser got %q, want %q", got, want)
	}
	if len(fell.opened) != 0 {
		t.Fatalf("the default opener opened %q, want nothing once $BROWSER opened the page", fell.opened)
	}
}

func TestABrowserCommandGetsTheURLWhereItsPlaceholderIsAndADoubledPercentAsOne(t *testing.T) {
	command, argv := recorder(t, "browser")

	if err := open(pageURL, command+" --url=%s '100%% sure'", (&fallback{}).open); err != nil {
		t.Fatal(err)
	}
	if got, want := received(t, argv), []string{"--url=" + pageURL, "100% sure"}; !slices.Equal(got, want) {
		t.Fatalf("the browser got %q, want %q", got, want)
	}
}

func TestTheFirstBrowserInTheListThatOpensThePageIsTheOnlyOneRun(t *testing.T) {
	first, firstArgv := recorder(t, "first")
	second, secondArgv := recorder(t, "second")
	list := strings.Join([]string{filepath.Join(t.TempDir(), "missing"), "false", first, second}, string(os.PathListSeparator))

	if err := open(pageURL, list, (&fallback{}).open); err != nil {
		t.Fatal(err)
	}
	if got := received(t, firstArgv); !slices.Equal(got, []string{pageURL}) {
		t.Fatalf("the first browser that runs got %q, want the URL", got)
	}
	if got := received(t, secondArgv); got != nil {
		t.Fatalf("the browser after it got %q, want it never run", got)
	}
}

func TestWhenNoBrowserInTheListOpensThePageTheDefaultOpenerDoes(t *testing.T) {
	var fell fallback

	if err := open(pageURL, "false"+string(os.PathListSeparator)+filepath.Join(t.TempDir(), "missing"), fell.open); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fell.opened, []string{pageURL}) {
		t.Fatalf("the default opener opened %q, want the page", fell.opened)
	}
}

func TestWithNoBrowserSetTheDefaultOpenerOpensThePage(t *testing.T) {
	var fell fallback

	if err := open(pageURL, "", fell.open); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fell.opened, []string{pageURL}) {
		t.Fatalf("the default opener opened %q, want the page", fell.opened)
	}
}

func TestABrowserOfTrueOrNoneOpensNothing(t *testing.T) {
	for _, browser := range []string{"true", "none"} {
		var fell fallback
		if err := open(pageURL, browser, fell.open); err != nil {
			t.Fatalf("BROWSER=%s: %v", browser, err)
		}
		if len(fell.opened) != 0 {
			t.Fatalf("BROWSER=%s: the default opener opened %q, want nothing", browser, fell.opened)
		}
	}
}
