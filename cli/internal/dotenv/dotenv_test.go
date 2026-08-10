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
	for _, ignored := range []string{"OCEL_DEV_SERVER", "database_url"} {
		if _, taken := file.Values[ignored]; taken {
			t.Errorf("%s was taken from the file; want it left to whatever else reads it", ignored)
		}
	}
	for _, taken := range []string{"NEXT_PUBLIC_SITE_URL", "AWS_PROFILE", "LAMBDA_TASK_ROOT"} {
		if _, ok := file.Values[taken]; !ok {
			t.Errorf("%s was left in the file; want a declarable key read from it", taken)
		}
	}
	if len(file.Unreadable) != 0 {
		t.Errorf("Unreadable = %v, want nothing: every line here is an assignment", file.Unreadable)
	}
}

func TestLoad_StopsAnUnquotedValueAtAHash(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "PORT=3000 # dev only\nQUOTED=\"3000 # kept\"\nTIGHT=3000#nospace\nTICK=`3000 # kept`\nESC=3000 \\# not-an-escape\nURL=http://x.com/#frag\nHASHONLY=#only\nMULTI=v # c # d\nESCQUOTE=\"a\\\"b#c\"\nESCTICK=`a\\`b#c`\n")

	file, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := map[string]string{
		"PORT":     "3000",
		"QUOTED":   "3000 # kept",
		"TIGHT":    "3000",
		"TICK":     "3000 # kept",
		"ESC":      `3000 \`,
		"URL":      "http://x.com/",
		"HASHONLY": "",
		"MULTI":    "v",
		"ESCQUOTE": `a\"b#c`,
		"ESCTICK":  "a\\`b#c",
	}
	for key, w := range want {
		if got := file.Values[key]; got != w {
			t.Errorf("%s = %q, want %q", key, got, w)
		}
	}
}

func TestLoad_AQuotedTokenOnlyWinsWhenNothingFollowsButAComment(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "TRAILING=\"a # b\" c\nBACKTRACK='#\\'# x'y\n")

	file, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := file.Values["TRAILING"], `"a`; got != want {
		t.Errorf("TRAILING = %q, want %q", got, want)
	}
	if got, want := file.Values["BACKTRACK"], `#\`; got != want {
		t.Errorf("BACKTRACK = %q, want %q", got, want)
	}
}

func TestLoad_UnescapesCarriageReturnInADoubleQuotedValue(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "A=\"a\\rb\"\nB='a\\rb'\nC=`a\\rb`\n")

	file, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := file.Values["A"], "a\rb"; got != want {
		t.Errorf("A = %q, want %q", got, want)
	}
	if got, want := file.Values["B"], `a\rb`; got != want {
		t.Errorf("B = %q, want %q (single quotes have no escapes)", got, want)
	}
	if got, want := file.Values["C"], `a\rb`; got != want {
		t.Errorf("C = %q, want %q (backtick quotes have no escapes)", got, want)
	}
}

func TestLoad_ReadsAnExportSeparatedByAnyWhitespace(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "export\tTABBED=1\nexport   SPACED=2\nexport\t  MIXED=3\nEXPORTED_NAME=4\nexportABC=5\nexport=6\n")

	file, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := map[string]string{"TABBED": "1", "SPACED": "2", "MIXED": "3", "EXPORTED_NAME": "4"}
	for key, w := range want {
		if got := file.Values[key]; got != w {
			t.Errorf("%s = %q, want %q", key, got, w)
		}
	}
	if len(file.Values) != len(want) {
		t.Errorf("Values = %v, want exactly %v", file.Values, want)
	}
	if _, taken := file.Values["ABC"]; taken {
		t.Errorf("Values = %v, want exportABC left alone: the keyword needs a separator", file.Values)
	}
	if len(file.Unreadable) != 0 {
		t.Errorf("Unreadable = %v, want nothing: every line here assigns", file.Unreadable)
	}
}

func TestLoad_ReadsPastAByteOrderMark(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "\ufeffDATABASE_URL=postgres://localhost/app\nAPI_TOKEN=t\nQUOTED=\"a # b\"\ufeff # c\nexport\ufeffBOMSEP=1\n\ufeff# a comment\n\ufeff\n")

	file, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := file.Values["DATABASE_URL"], "postgres://localhost/app"; got != want {
		t.Errorf("DATABASE_URL = %q, want %q", got, want)
	}
	if got, want := file.Values["API_TOKEN"], "t"; got != want {
		t.Errorf("API_TOKEN = %q, want %q", got, want)
	}
	if got, want := file.Values["QUOTED"], "a # b"; got != want {
		t.Errorf("QUOTED = %q, want %q", got, want)
	}
	if got, want := file.Values["BOMSEP"], "1"; got != want {
		t.Errorf("BOMSEP = %q, want %q", got, want)
	}
	if len(file.Unreadable) != 0 {
		t.Errorf("Unreadable = %v, want nothing", file.Unreadable)
	}
}

func TestLoad_SplitsLinesOnALoneCarriageReturn(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "A_KEY=1\rB_KEY=2\r")

	file, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := map[string]string{"A_KEY": "1", "B_KEY": "2"}
	for key, w := range want {
		if got := file.Values[key]; got != w {
			t.Errorf("%s = %q, want %q", key, got, w)
		}
	}
	if len(file.Values) != len(want) {
		t.Errorf("Values = %v, want exactly %v", file.Values, want)
	}
	if len(file.Unreadable) != 0 {
		t.Errorf("Unreadable = %v, want nothing", file.Unreadable)
	}
}

func TestLoad_ReadsALastLineWithNoTerminator(t *testing.T) {
	for name, contents := range map[string]string{
		"lf":  "A_KEY=1\nB_KEY=2",
		"cr":  "A_KEY=1\rB_KEY=2",
		"one": "B_KEY=2",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, contents)

			file, err := Load(dir)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got, want := file.Values["B_KEY"], "2"; got != want {
				t.Errorf("B_KEY = %q, want %q: the last line needs no terminator", got, want)
			}
			if len(file.Unreadable) != 0 {
				t.Errorf("Unreadable = %v, want nothing", file.Unreadable)
			}
		})
	}
}

func TestLoad_TreatsNextLineAsPartOfTheLineNotAsWhitespace(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "export\u0085EXPORTED=1\n\u0085LEADING=2\nTAIL=3\u0085\nQUOTED=\"a # b\"\u0085#c\n")

	file, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, taken := file.Values["EXPORTED"]; taken {
		t.Errorf("Values = %v, want export\\u0085EXPORTED left alone: NEL is not a separator", file.Values)
	}
	if _, taken := file.Values["LEADING"]; taken {
		t.Errorf("Values = %v, want \\u0085LEADING left alone: NEL does not start a key", file.Values)
	}
	if got, want := file.Values["TAIL"], "3\u0085"; got != want {
		t.Errorf("TAIL = %q, want %q: a trailing NEL is part of the value", got, want)
	}
	if got, want := file.Values["QUOTED"], "\"a"; got != want {
		t.Errorf("QUOTED = %q, want %q: NEL after a close fails the tail check", got, want)
	}
}

func TestLoad_NumbersUnreadableLinesTheSameUnderEveryLineEnding(t *testing.T) {
	for name, contents := range map[string]string{
		"lf":   "A_KEY=1\nsk-live-must-not-appear\nB_KEY=2\n",
		"crlf": "A_KEY=1\r\nsk-live-must-not-appear\r\nB_KEY=2\r\n",
		"cr":   "A_KEY=1\rsk-live-must-not-appear\rB_KEY=2\r",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, contents)

			file, err := Load(dir)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if len(file.Unreadable) != 1 || file.Unreadable[0] != 2 {
				t.Errorf("Unreadable = %v, want [2]", file.Unreadable)
			}
			if file.Values["A_KEY"] != "1" || file.Values["B_KEY"] != "2" {
				t.Errorf("Values = %v, want both keys read around the unreadable line", file.Values)
			}
		})
	}
}

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
