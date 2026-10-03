package logview_test

import (
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/logview"
)

func TestJoinGroupsStackTraceLinesWithTheirEntry(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		lines []string
		want  [][]string
	}{
		{
			name:  "plain lines stay apart",
			lines: []string{"one", "two"},
			want:  [][]string{{"one"}, {"two"}},
		},
		{
			name:  "no lines",
			lines: nil,
			want:  nil,
		},
		{
			name: "node error stack",
			lines: []string{
				"before",
				"Error: boom",
				"    at run (/app/index.js:3:9)",
				"    at Object.<anonymous> (/app/index.js:7:1)",
				"after",
			},
			want: [][]string{
				{"before"},
				{"Error: boom", "    at run (/app/index.js:3:9)", "    at Object.<anonymous> (/app/index.js:7:1)"},
				{"after"},
			},
		},
		{
			name: "python traceback",
			lines: []string{
				"before",
				"Traceback (most recent call last):",
				`  File "/app/main.py", line 4, in <module>`,
				"    main()",
				`  File "/app/main.py", line 2, in main`,
				"    raise ValueError(\"boom\")",
				"ValueError: boom",
				"after",
			},
			want: [][]string{
				{"before"},
				{
					"Traceback (most recent call last):",
					`  File "/app/main.py", line 4, in <module>`,
					"    main()",
					`  File "/app/main.py", line 2, in main`,
					"    raise ValueError(\"boom\")",
					"ValueError: boom",
				},
				{"after"},
			},
		},
		{
			name: "go panic",
			lines: []string{
				"before",
				"panic: boom",
				"",
				"goroutine 1 [running]:",
				"main.main()",
				"\t/app/main.go:10 +0x1d",
				"exit status 2",
				"after",
			},
			want: [][]string{
				{"before"},
				{
					"panic: boom",
					"",
					"goroutine 1 [running]:",
					"main.main()",
					"\t/app/main.go:10 +0x1d",
					"exit status 2",
				},
				{"after"},
			},
		},
		{
			name: "go goroutine dump without a panic line",
			lines: []string{
				"before",
				"goroutine 7 [chan receive]:",
				"main.worker(0x1)",
				"\t/app/w.go:5 +0x2",
				"created by main.main",
				"\t/app/main.go:9 +0x3",
				"after",
			},
			want: [][]string{
				{"before"},
				{
					"goroutine 7 [chan receive]:",
					"main.worker(0x1)",
					"\t/app/w.go:5 +0x2",
					"created by main.main",
					"\t/app/main.go:9 +0x3",
				},
				{"after"},
			},
		},
		{
			name: "two goroutines stay in one group",
			lines: []string{
				"goroutine 1 [running]:",
				"main.a()",
				"\t/a.go:1 +0x1",
				"",
				"goroutine 2 [select]:",
				"main.b()",
				"\t/b.go:1 +0x1",
				"after",
			},
			want: [][]string{
				{
					"goroutine 1 [running]:",
					"main.a()",
					"\t/a.go:1 +0x1",
					"",
					"goroutine 2 [select]:",
					"main.b()",
					"\t/b.go:1 +0x1",
				},
				{"after"},
			},
		},
		{
			name: "rust backtrace",
			lines: []string{
				"before",
				"thread 'main' panicked at src/main.rs:2:5:",
				"boom",
				"stack backtrace:",
				"   0: rust_begin_unwind",
				"             at /rustc/abc/library/std/src/panicking.rs:645:5",
				"   1: app::main",
				"note: Some details are omitted, run with `RUST_BACKTRACE=full` for a verbose backtrace.",
				"after",
			},
			want: [][]string{
				{"before"},
				{
					"thread 'main' panicked at src/main.rs:2:5:",
					"boom",
					"stack backtrace:",
					"   0: rust_begin_unwind",
					"             at /rustc/abc/library/std/src/panicking.rs:645:5",
					"   1: app::main",
					"note: Some details are omitted, run with `RUST_BACKTRACE=full` for a verbose backtrace.",
				},
				{"after"},
			},
		},
		{
			name: "a backtrace header without a panic line starts a group",
			lines: []string{
				"before",
				"stack backtrace:",
				"   0: app::main",
				"after",
			},
			want: [][]string{
				{"before"},
				{"stack backtrace:", "   0: app::main"},
				{"after"},
			},
		},
		{
			name: "a traceback starts a new group even inside another trace",
			lines: []string{
				"Error: a",
				"    at x (a.js:1:1)",
				"Traceback (most recent call last):",
				`  File "a.py", line 1, in <module>`,
				"KeyError: 'k'",
			},
			want: [][]string{
				{"Error: a", "    at x (a.js:1:1)"},
				{"Traceback (most recent call last):", `  File "a.py", line 1, in <module>`, "KeyError: 'k'"},
			},
		},
		{
			name:  "a continuation first in the input starts its own group",
			lines: []string{"    at orphan (a.js:1:1)", "next"},
			want:  [][]string{{"    at orphan (a.js:1:1)"}, {"next"}},
		},
		{
			name:  "an indented line that is not a frame is its own entry",
			lines: []string{"one", "  two"},
			want:  [][]string{{"one"}, {"  two"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := logview.Join(tc.lines); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Join =\n got  %q\n want %q", got, tc.want)
			}
		})
	}
}
