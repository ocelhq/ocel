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

	"github.com/ocelhq/ocel/cli/internal/dotfile"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func dotfileValues(values map[string]string) valueLayers {
	return valueLayers{{from: dotfile.FileName, file: true, values: values}}
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

	t.Run("it states what the file costs and prints no value", func(t *testing.T) {
		t.Parallel()

		var quiet bytes.Buffer
		reportValues(spanWriting(t, &quiet), t.TempDir(), dotfileValues(nil), true)
		if quiet.Len() != 0 {
			t.Errorf("reportValues wrote %q for a run with no dotfile values, want nothing", quiet.String())
		}

		dir := gitRepository(t)
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".env\n"), 0o644); err != nil {
			t.Fatalf("write .gitignore: %v", err)
		}

		var out bytes.Buffer
		reportValues(spanWriting(t, &out), dir, dotfileValues(map[string]string{"API_TOKEN": "sk-live-must-not-appear", "DATABASE_URL": "postgres://secret"}), true)
		got := out.String()

		for _, want := range []string{"API_TOKEN", "DATABASE_URL", dotfile.FileName} {
			if !strings.Contains(got, want) {
				t.Errorf("notice = %q, want it to mention %q", got, want)
			}
		}
		for _, leaked := range []string{"sk-live", "postgres://secret"} {
			if strings.Contains(got, leaked) {
				t.Fatalf("notice = %q, want it to disclose no value", got)
			}
		}
		if !strings.Contains(got, "teammate") && !strings.Contains(got, "yours alone") {
			t.Errorf("notice = %q, want it to say the collaboration a shared store provides is gone", got)
		}
		if !strings.Contains(got, "plaintext") {
			t.Errorf("notice = %q, want it to say values reach the child in plaintext, which a deploy does not do", got)
		}
		if strings.Contains(got, ".gitignore") {
			t.Errorf("notice = %q, want no gitignore warning when the file is already ignored", got)
		}
	})

	t.Run("it warns when the file is not ignored", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer
		reportValues(spanWriting(t, &out), gitRepository(t), dotfileValues(map[string]string{"API_TOKEN": "x"}), true)

		if got := out.String(); !strings.Contains(got, ".gitignore") {
			t.Errorf("notice = %q, want it to say the file is not ignored by git", got)
		}

		dir := gitRepository(t)
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".env*\n!.env\n"), 0o644); err != nil {
			t.Fatalf("write .gitignore: %v", err)
		}
		var reincluded bytes.Buffer
		reportValues(spanWriting(t, &reincluded), dir, dotfileValues(map[string]string{"API_TOKEN": "x"}), true)
		if got := reincluded.String(); !strings.Contains(got, ".gitignore") {
			t.Errorf("notice = %q, want the warning when a later line re-includes the file", got)
		}
	})

	t.Run("it names where each value came from, and checks "+dotfile.LocalFileName+" against .gitignore on its own", func(t *testing.T) {
		t.Parallel()

		dir := gitRepository(t)
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".env\n"), 0o644); err != nil {
			t.Fatalf("write .gitignore: %v", err)
		}
		var out bytes.Buffer
		reportValues(spanWriting(t, &out), dir, valueLayers{
			{from: "infisical:p-1/dev", values: map[string]string{"API_TOKEN": "sk-live-must-not-appear"}},
			{from: dotfile.LocalFileName, file: true, values: map[string]string{"LOG_LEVEL": "debug"}},
		}, true)
		got := out.String()

		for _, want := range []string{"API_TOKEN from infisical:p-1/dev", "LOG_LEVEL from " + dotfile.LocalFileName, dotfile.LocalFileName + " is not ignored by git", "editing " + dotfile.LocalFileName + " re-resolves"} {
			if !strings.Contains(got, want) {
				t.Errorf("notice = %q, want it to say %q", got, want)
			}
		}
		if strings.Contains(got, "sk-live") {
			t.Fatalf("notice = %q, want it to disclose no value", got)
		}

		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.local\n"), 0o644); err != nil {
			t.Fatalf("write .gitignore: %v", err)
		}
		var ignored bytes.Buffer
		reportValues(spanWriting(t, &ignored), dir, valueLayers{{from: dotfile.LocalFileName, file: true, values: map[string]string{"LOG_LEVEL": "debug"}}}, false)
		if strings.Contains(ignored.String(), ".gitignore") {
			t.Errorf("notice = %q, want no warning for a file a glob ignores", ignored.String())
		}
	})
}

func gitRepository(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return dir
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
		reportValues(spanWriting(t, &out), project, dotfileValues(map[string]string{"API_TOKEN": "x"}), true)
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
		reportValues(spanWriting(t, &out), dir, dotfileValues(map[string]string{"API_TOKEN": "x"}), true)
		if got := out.String(); !strings.Contains(got, "cannot tell whether git ignores "+dotfile.FileName) {
			t.Errorf("notice = %q, want it to say it cannot tell", got)
		}
	})
}

func TestReportUnreadableLines(t *testing.T) {
	t.Parallel()

	t.Run("it names them by number only", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer
		reportUnreadableLines(spanWriting(t, &out), valueLayers{{from: dotfile.FileName, file: true, unreadable: []int{2, 5}}})
		got := out.String()

		for _, want := range []string{dotfile.FileName, "2, 5"} {
			if !strings.Contains(got, want) {
				t.Errorf("notice = %q, want it to mention %q", got, want)
			}
		}
	})

	t.Run("a single line is reported singularly", func(t *testing.T) {
		t.Parallel()

		var one bytes.Buffer
		reportUnreadableLines(spanWriting(t, &one), valueLayers{{from: dotfile.LocalFileName, file: true, unreadable: []int{4}}})
		if !strings.Contains(one.String(), dotfile.LocalFileName) {
			t.Errorf("notice = %q, want the file named", one.String())
		}
		if !strings.Contains(one.String(), "line 4 is") {
			t.Errorf("notice = %q, want a singular line reported singularly", one.String())
		}
	})
}

func TestReportSecretValues(t *testing.T) {
	t.Parallel()

	t.Run("a run with no secret values says nothing", func(t *testing.T) {
		t.Parallel()

		var quiet bytes.Buffer
		reportSecretValues(spanWriting(t, &quiet), nil)
		if quiet.Len() != 0 {
			t.Errorf("reportSecretValues wrote %q for a run with no secret values, want nothing", quiet.String())
		}
	})

	t.Run("it names every secret key and says dev resolves them like any other value", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer
		reportSecretValues(spanWriting(t, &out), []string{"WEBHOOK_SECRET", "API_TOKEN"})
		got := out.String()
		for _, want := range []string{"API_TOKEN", "WEBHOOK_SECRET", "every other value", "bounded window"} {
			if !strings.Contains(got, want) {
				t.Errorf("reportSecretValues wrote %q, want it to mention %q", got, want)
			}
		}
	})
}
