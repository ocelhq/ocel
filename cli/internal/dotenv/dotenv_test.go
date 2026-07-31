package dotenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", FileName, err)
	}
}

// A project with no required variables must still run, so an absent dotfile is
// the ordinary case rather than a failure.
func TestLoad_AbsentFileIsNotAnError(t *testing.T) {
	file, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(file.Values) != 0 {
		t.Fatalf("Load = %v, want empty", file.Values)
	}
}

func TestLoad_ParsesTheFlatForms(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, `
# a whole-line comment
DATABASE_URL=postgres://localhost/app

  SPACED  =  padded
QUOTED="a value # not a comment"
UNBALANCED="still open
SINGLE='literal $NOT_EXPANDED'
ESCAPED="first\nsecond"
EMPTY=
EQUALS=a=b
export EXPORTED=sourced
`)

	file, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	values := file.Values

	want := map[string]string{
		"DATABASE_URL": "postgres://localhost/app",
		"SPACED":       "padded",
		"QUOTED":       "a value # not a comment",
		"UNBALANCED":   `"still open`,
		"SINGLE":       "literal $NOT_EXPANDED",
		"ESCAPED":      "first\nsecond",
		"EMPTY":        "",
		"EQUALS":       "a=b",
		"EXPORTED":     "sourced",
	}
	for key, w := range want {
		got, ok := values[key]
		if !ok {
			t.Errorf("%s missing from %v", key, values)
			continue
		}
		if got != w {
			t.Errorf("%s = %q, want %q", key, got, w)
		}
	}
	if len(values) != len(want) {
		t.Errorf("Load returned %d values, want %d: %v", len(values), len(want), values)
	}
}

// A value's origin is one answer, so a dotfile line never refers to another
// value. Interpolation is not a supported form and the '$' is literal.
func TestLoad_DoesNotInterpolate(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "BASE=one\nDERIVED=$BASE/two\n")

	file, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if file.Values["DERIVED"] != "$BASE/two" {
		t.Fatalf("DERIVED = %q, want the literal %q", file.Values["DERIVED"], "$BASE/two")
	}
}

// `.env` is the file the framework already reads, so most of it is not Ocel's.
// A key Ocel could never be asked for is ignored where it stands and the rest of
// the file still loads — refusing the run over one would refuse the first
// `ocel dev` in every project that already has this file.
func TestLoad_IgnoresTheLinesItDoesNotOwn(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, `
NEXT_PUBLIC_SITE_URL=https://example.com
AWS_PROFILE=dev
LAMBDA_TASK_ROOT=/var/task
OCEL_DEV_SERVER=hijacked
database_url=lower
DATABASE_URL=postgres://localhost/app
`)

	file, err := Load(dir)
	if err != nil {
		t.Fatalf("Load = %v, want a file Ocel does not own to be read past, not refused", err)
	}
	if file.Values["DATABASE_URL"] != "postgres://localhost/app" {
		t.Fatalf("DATABASE_URL = %q, want the line Ocel does own still read", file.Values["DATABASE_URL"])
	}
	for _, ignored := range []string{"NEXT_PUBLIC_SITE_URL", "AWS_PROFILE", "LAMBDA_TASK_ROOT", "OCEL_DEV_SERVER", "database_url"} {
		if _, taken := file.Values[ignored]; taken {
			t.Errorf("%s was taken from the file; want it left to whatever else reads it", ignored)
		}
	}
	if len(file.Unreadable) != 0 {
		t.Errorf("Unreadable = %v, want nothing: every line here is an assignment", file.Unreadable)
	}
}

// A key set twice is not ambiguous enough to stop a run over: the dotenv parser
// the framework uses answers with the last one, so answering differently would
// mean the app and Ocel read the same file two ways.
func TestLoad_TakesTheLastOfARepeatedKey(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "API_TOKEN=first\nAPI_TOKEN=second\n")

	file, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if file.Values["API_TOKEN"] != "second" {
		t.Fatalf("API_TOKEN = %q, want the last assignment", file.Values["API_TOKEN"])
	}
}

// A line that assigns nothing is nobody's — not Ocel's and not the framework's —
// so it is reported rather than passed over in silence, which is what makes a
// pasted token or a typo visible.
func TestLoad_ReportsTheLinesThatAssignNothing(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "DATABASE_URL=postgres://localhost/app\nsk-live-must-not-appear\n\n# comment\nalso nothing\n")

	file, err := Load(dir)
	if err != nil {
		t.Fatalf("Load = %v, want an unreadable line to be reported, not to fail the file", err)
	}
	if file.Values["DATABASE_URL"] != "postgres://localhost/app" {
		t.Fatalf("DATABASE_URL = %q, want the readable lines still read", file.Values["DATABASE_URL"])
	}
	want := []int{2, 5}
	if len(file.Unreadable) != len(want) || file.Unreadable[0] != want[0] || file.Unreadable[1] != want[1] {
		t.Fatalf("Unreadable = %v, want %v", file.Unreadable, want)
	}
}

// Nothing the parser hands back carries a line's contents: what it could not
// read is reported by number, because an unreadable line is exactly the shape a
// pasted token has and this is the one file whose contents nothing else may see.
func TestLoad_NeverHandsBackALinesContents(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "sk-live-must-not-appear\nexport sk-live-either\n")

	file, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(file.Unreadable) == 0 {
		t.Fatal("Unreadable is empty, want the lines that assign nothing reported")
	}
	if len(file.Values) != 0 {
		t.Fatalf("Values = %v, want nothing taken from lines that assign nothing", file.Values)
	}
	if strings.Contains(fmt.Sprint(file), "sk-live") {
		t.Fatalf("file = %v, want it to disclose no line contents", file)
	}
}
