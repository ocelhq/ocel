package dotenv

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func write(t *testing.T, dir, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", FileName, err)
	}
}

func load(t *testing.T, contents string) File {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, contents)
	file, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return file
}

type parseCase struct {
	name       string
	contents   string
	want       map[string]string
	absent     []string
	exhaustive bool
	unreadable []int
	note       string
}

func (tc parseCase) run(t *testing.T) {
	t.Helper()
	file := load(t, tc.contents)

	for key, want := range tc.want {
		got, ok := file.Values[key]
		if !ok {
			t.Errorf("%s missing from %v%s", key, file.Values, tc.because())
			continue
		}
		if got != want {
			t.Errorf("%s = %q, want %q%s", key, got, want, tc.because())
		}
	}
	for _, key := range tc.absent {
		if _, taken := file.Values[key]; taken {
			t.Errorf("Values = %v, want %s left alone%s", file.Values, key, tc.because())
		}
	}
	if tc.exhaustive && len(file.Values) != len(tc.want) {
		t.Errorf("Values = %v, want exactly %v%s", file.Values, tc.want, tc.because())
	}
	if !slices.Equal(file.Unreadable, tc.unreadable) {
		want := "nothing"
		if len(tc.unreadable) > 0 {
			want = fmt.Sprint(tc.unreadable)
		}
		t.Errorf("Unreadable = %v, want %s%s", file.Unreadable, want, tc.because())
	}
}

func (tc parseCase) because() string {
	if tc.note == "" {
		return ""
	}
	return ": " + tc.note
}

func TestLoad(t *testing.T) {
	t.Parallel()

	t.Run("an absent file is not an error", func(t *testing.T) {
		t.Parallel()
		file, err := Load(t.TempDir())
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(file.Values) != 0 {
			t.Fatalf("Load = %v, want empty", file.Values)
		}
	})

	const quotesAndEscapes = "A=\"a\\rb\"\nB='a\\rb'\nC=`a\\rb`\n"
	const nextLine = "export\u0085EXPORTED=1\n\u0085LEADING=2\nTAIL=3\u0085\nQUOTED=\"a # b\"\u0085#c\n"

	for _, tc := range []parseCase{
		{
			name: "parses the flat forms",
			contents: `
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
`,
			want: map[string]string{
				"DATABASE_URL": "postgres://localhost/app",
				"SPACED":       "padded",
				"QUOTED":       "a value # not a comment",
				"UNBALANCED":   `"still open`,
				"SINGLE":       "literal $NOT_EXPANDED",
				"ESCAPED":      "first\nsecond",
				"EMPTY":        "",
				"EQUALS":       "a=b",
				"EXPORTED":     "sourced",
			},
			exhaustive: true,
		},
		{
			name:       "does not interpolate",
			contents:   "BASE=one\nDERIVED=$BASE/two\n",
			want:       map[string]string{"BASE": "one", "DERIVED": "$BASE/two"},
			exhaustive: true,
			note:       "the value is taken literally",
		},
		{
			name: "ignores the lines it does not own",
			contents: `
NEXT_PUBLIC_SITE_URL=https://example.com
AWS_PROFILE=dev
LAMBDA_TASK_ROOT=/var/task
OCEL_DEV_SERVER=hijacked
database_url=lower
DATABASE_URL=postgres://localhost/app
`,
			want: map[string]string{
				"DATABASE_URL":         "postgres://localhost/app",
				"NEXT_PUBLIC_SITE_URL": "https://example.com",
				"AWS_PROFILE":          "dev",
				"LAMBDA_TASK_ROOT":     "/var/task",
			},
			absent:     []string{"OCEL_DEV_SERVER", "database_url"},
			exhaustive: true,
			note:       "a file Ocel does not own is read past, not refused; a declarable key is still read, and the rest is left to whatever else reads it",
		},
		{
			name:     "stops an unquoted value at a hash",
			contents: "PORT=3000 # dev only\nQUOTED=\"3000 # kept\"\nTIGHT=3000#nospace\nTICK=`3000 # kept`\nESC=3000 \\# not-an-escape\nURL=http://x.com/#frag\nHASHONLY=#only\nMULTI=v # c # d\nESCQUOTE=\"a\\\"b#c\"\nESCTICK=`a\\`b#c`\n",
			want: map[string]string{
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
			},
			exhaustive: true,
		},
		{
			name:       "a quoted token only wins when nothing follows but a comment",
			contents:   "TRAILING=\"a # b\" c\nBACKTRACK='#\\'# x'y\n",
			want:       map[string]string{"TRAILING": `"a`, "BACKTRACK": `#\`},
			exhaustive: true,
		},
		{
			name:     "unescapes carriage return in a double quoted value",
			contents: quotesAndEscapes,
			want:     map[string]string{"A": "a\rb"},
		},
		{
			name:     "leaves the escape alone in a single quoted value",
			contents: quotesAndEscapes,
			want:     map[string]string{"B": `a\rb`},
			note:     "single quotes have no escapes",
		},
		{
			name:     "leaves the escape alone in a backtick quoted value",
			contents: quotesAndEscapes,
			want:     map[string]string{"C": `a\rb`},
			note:     "backtick quotes have no escapes",
		},
		{
			name:       "reads an export separated by any whitespace",
			contents:   "export\tTABBED=1\nexport   SPACED=2\nexport\t  MIXED=3\nEXPORTED_NAME=4\nexportABC=5\nexport=6\n",
			want:       map[string]string{"TABBED": "1", "SPACED": "2", "MIXED": "3", "EXPORTED_NAME": "4"},
			absent:     []string{"ABC"},
			exhaustive: true,
			note:       "the keyword needs a separator, and every line here assigns",
		},
		{
			name:     "reads past a byte order mark",
			contents: "\ufeffDATABASE_URL=postgres://localhost/app\nAPI_TOKEN=t\nQUOTED=\"a # b\"\ufeff # c\nexport\ufeffBOMSEP=1\n\ufeff# a comment\n\ufeff\n",
			want: map[string]string{
				"DATABASE_URL": "postgres://localhost/app",
				"API_TOKEN":    "t",
				"QUOTED":       "a # b",
				"BOMSEP":       "1",
			},
			exhaustive: true,
		},
		{
			name:       "splits lines on a lone carriage return",
			contents:   "A_KEY=1\rB_KEY=2\r",
			want:       map[string]string{"A_KEY": "1", "B_KEY": "2"},
			exhaustive: true,
		},
		{
			name:       "reads a last line with no terminator, after a line feed",
			contents:   "A_KEY=1\nB_KEY=2",
			want:       map[string]string{"A_KEY": "1", "B_KEY": "2"},
			exhaustive: true,
			note:       "the last line needs no terminator",
		},
		{
			name:       "reads a last line with no terminator, after a carriage return",
			contents:   "A_KEY=1\rB_KEY=2",
			want:       map[string]string{"A_KEY": "1", "B_KEY": "2"},
			exhaustive: true,
			note:       "the last line needs no terminator",
		},
		{
			name:       "reads a last line with no terminator, when it is the only line",
			contents:   "B_KEY=2",
			want:       map[string]string{"B_KEY": "2"},
			exhaustive: true,
			note:       "the last line needs no terminator",
		},
		{
			name:     "treats next line as part of the line, not as a separator after export",
			contents: nextLine,
			absent:   []string{"EXPORTED"},
			note:     "NEL is not a separator, so export\\u0085EXPORTED is left alone",
		},
		{
			name:     "treats next line as part of the line, not as the start of a key",
			contents: nextLine,
			absent:   []string{"LEADING"},
			note:     "NEL does not start a key, so \\u0085LEADING is left alone",
		},
		{
			name:     "treats next line as part of the line, keeping a trailing NEL in the value",
			contents: nextLine,
			want:     map[string]string{"TAIL": "3\u0085"},
			note:     "a trailing NEL is part of the value",
		},
		{
			name:     "treats next line as part of the line, failing the tail check after a close quote",
			contents: nextLine,
			want:     map[string]string{"QUOTED": `"a`},
			note:     "NEL after a close fails the tail check",
		},
		{
			name:       "numbers unreadable lines the same under line feeds",
			contents:   "A_KEY=1\nsk-live-must-not-appear\nB_KEY=2\n",
			want:       map[string]string{"A_KEY": "1", "B_KEY": "2"},
			exhaustive: true,
			unreadable: []int{2},
			note:       "both keys are read around the unreadable line",
		},
		{
			name:       "numbers unreadable lines the same under carriage return line feeds",
			contents:   "A_KEY=1\r\nsk-live-must-not-appear\r\nB_KEY=2\r\n",
			want:       map[string]string{"A_KEY": "1", "B_KEY": "2"},
			exhaustive: true,
			unreadable: []int{2},
			note:       "both keys are read around the unreadable line",
		},
		{
			name:       "numbers unreadable lines the same under lone carriage returns",
			contents:   "A_KEY=1\rsk-live-must-not-appear\rB_KEY=2\r",
			want:       map[string]string{"A_KEY": "1", "B_KEY": "2"},
			exhaustive: true,
			unreadable: []int{2},
			note:       "both keys are read around the unreadable line",
		},
		{
			name:       "takes the last of a repeated key",
			contents:   "API_TOKEN=first\nAPI_TOKEN=second\n",
			want:       map[string]string{"API_TOKEN": "second"},
			exhaustive: true,
			note:       "the last assignment wins",
		},
		{
			name:       "reports the lines that assign nothing",
			contents:   "DATABASE_URL=postgres://localhost/app\nsk-live-must-not-appear\n\n# comment\nalso nothing\n",
			want:       map[string]string{"DATABASE_URL": "postgres://localhost/app"},
			exhaustive: true,
			unreadable: []int{2, 5},
			note:       "an unreadable line is reported, not a failure of the file, and the readable lines are still read",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.run(t)
		})
	}

	t.Run("never hands back a line's contents", func(t *testing.T) {
		t.Parallel()
		file := load(t, "sk-live-must-not-appear\nexport sk-live-either\n")

		if len(file.Unreadable) == 0 {
			t.Fatal("Unreadable is empty, want the lines that assign nothing reported")
		}
		if len(file.Values) != 0 {
			t.Fatalf("Values = %v, want nothing taken from lines that assign nothing", file.Values)
		}
		if strings.Contains(fmt.Sprint(file), "sk-live") {
			t.Fatalf("file = %v, want it to disclose no line contents", file)
		}
	})
}
