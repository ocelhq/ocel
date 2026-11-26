package dev

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/project"

	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func dotfileValues(values map[string]string) valueLayers {
	return valueLayers{{from: project.DotenvFileName, file: true, values: values}}
}

func spanWriting(t *testing.T, w io.Writer) *run.Span {
	t.Helper()
	bus := run.NewBus(time.Now)
	bus.Attach(terminal.NewTranscript(w, terminal.Resolve(terminal.Conditions{})))
	t.Cleanup(func() { _ = bus.Close() })
	_, begun, err := bus.Begin(context.Background(), "ocel dev", "")
	if err != nil {
		t.Fatalf("begin a run: %v", err)
	}
	return begun.Phase(progressv1.Phase_PHASE_UNSPECIFIED)
}

func TestTheDevValueReportNamesWhereEachKeyCameFromAndNeverAValue(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		layers valueLayers
		want   string
	}{
		{name: "empty", want: "0 variables"},
		{name: "single", layers: dotfileValues(map[string]string{"API_TOKEN": "secret"}), want: "1 variable from .env"},
		{name: "multiple", layers: dotfileValues(map[string]string{"API_TOKEN": "secret", "DATABASE_URL": "postgres://secret"}), want: "2 variables from .env"},
		{name: "overlapping sources", layers: valueLayers{
			{from: "infisical", values: map[string]string{"API_TOKEN": "shared", "DATABASE_URL": "postgres://secret"}},
			{from: ".env.local", values: map[string]string{"API_TOKEN": "local"}},
		}, want: "2 variables from infisical, .env.local"},
		{name: "fully overridden source", layers: valueLayers{
			{from: ".env", values: map[string]string{"API_TOKEN": "shared"}},
			{from: ".env.local", values: map[string]string{"API_TOKEN": "local"}},
		}, want: "1 variable from .env.local"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.layers.summary(); got != tc.want {
				t.Errorf("summary = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTheDevValueReportWarnsOnlyForUnignoredFiles(t *testing.T) {
	t.Parallel()
	dir := gitRepository(t)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	warnUnignoredFiles(t.Context(), spanWriting(t, &out), dir, valueLayers{
		{from: "infisical", values: map[string]string{"API_TOKEN": "secret"}},
		{from: ".env", file: true, values: map[string]string{"API_TOKEN": "secret"}},
		{from: ".env.local", file: true, values: map[string]string{"API_TOKEN": "secret"}},
	})
	got := out.String()
	if !strings.Contains(got, ".env.local is not ignored by git") {
		t.Fatalf("warnings = %q, want the unignored file named", got)
	}
	for _, unwanted := range []string{"infisical", ".env is not ignored", "API_TOKEN", "secret", "teammate", "plaintext"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("warnings = %q, want no %q", got, unwanted)
		}
	}
}

func gitRepository(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return dir
}

func TestTheDevValueReportTestsLeaveAnInheritedGitDirUntouched(t *testing.T) {
	t.Parallel()

	inherited := filepath.Join(t.TempDir(), "inherited.git")
	selected := []string{
		"TestTheDevValueReportNamesWhereEachKeyCameFromAndNeverAValue",
		"TestTheDevValueReportAsksGitWhetherItIgnoresEachFile",
	}
	child := exec.Command(os.Args[0], "-test.v", "-test.run", "^("+strings.Join(selected, "|")+")$")
	child.Env = append(os.Environ(), "GIT_DIR="+inherited)
	out, err := child.CombinedOutput()
	if err != nil {
		t.Errorf("the report tests under GIT_DIR=%s failed: %v\n%s", inherited, err, out)
	}
	for _, name := range selected {
		if !strings.Contains(string(out), "\n--- PASS: "+name+" (") {
			t.Errorf("the child run has no passing %s, want it run and passed\n%s", name, out)
		}
	}
	if _, err := os.Stat(inherited); !os.IsNotExist(err) {
		t.Errorf("the report tests wrote to the inherited GIT_DIR %s (stat: %v), want it untouched", inherited, err)
	}
}

func TestTheDevValueReportAsksGitWhetherItIgnoresEachFile(t *testing.T) {
	t.Parallel()

	t.Run("a pattern in the repository's root .gitignore covers a project in a subdirectory", func(t *testing.T) {
		t.Parallel()
		repository := gitRepository(t)
		if err := os.WriteFile(filepath.Join(repository, ".gitignore"), []byte("**/.env\n"), 0o644); err != nil {
			t.Fatalf("write .gitignore: %v", err)
		}
		project := filepath.Join(repository, "apps", "web")
		if err := os.MkdirAll(project, 0o755); err != nil {
			t.Fatal(err)
		}

		var out bytes.Buffer
		warnUnignoredFiles(t.Context(), spanWriting(t, &out), project, dotfileValues(map[string]string{"API_TOKEN": "x"}))
		if strings.Contains(out.String(), ".gitignore") {
			t.Errorf("notice = %q, want no warning for a file git ignores", out.String())
		}
	})

	t.Run("outside a git repository it says it cannot tell", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".env\n"), 0o644); err != nil {
			t.Fatalf("write .gitignore: %v", err)
		}

		var out bytes.Buffer
		warnUnignoredFiles(t.Context(), spanWriting(t, &out), dir, dotfileValues(map[string]string{"API_TOKEN": "x"}))
		if got := out.String(); !strings.Contains(got, "Could not check whether git ignores "+project.DotenvFileName) {
			t.Errorf("notice = %q, want it to say it cannot tell", got)
		}
	})
}

func TestReportUnreadableLines(t *testing.T) {
	t.Parallel()

	t.Run("it names them by number only", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer
		reportUnreadableLines(spanWriting(t, &out), valueLayers{{from: project.DotenvFileName, file: true, unreadable: []int{2, 5}}})
		got := out.String()

		for _, want := range []string{project.DotenvFileName, "2, 5"} {
			if !strings.Contains(got, want) {
				t.Errorf("notice = %q, want it to mention %q", got, want)
			}
		}
	})

	t.Run("a single line is reported singularly", func(t *testing.T) {
		t.Parallel()

		var one bytes.Buffer
		reportUnreadableLines(spanWriting(t, &one), valueLayers{{from: project.LocalDotenvFileName, file: true, unreadable: []int{4}}})
		if !strings.Contains(one.String(), project.LocalDotenvFileName) {
			t.Errorf("notice = %q, want the file named", one.String())
		}
		if !strings.Contains(one.String(), "line 4 is") {
			t.Errorf("notice = %q, want a singular line reported singularly", one.String())
		}
	})
}
