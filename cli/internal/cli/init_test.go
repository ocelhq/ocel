package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

func stubPackageManager(deps *cmddeps.Deps, result error) *[]string {
	var argv []string
	deps.RunPackageManager = func(_ context.Context, _ string, cmd []string, _ io.Writer) error {
		argv = cmd
		return result
	}
	return &argv
}

func initDeps() cmddeps.Deps {
	deps := newDeps()
	deps.Events = run.NewBus(time.Now)
	return deps
}

func TestAddingTheSDKIsAUnitOnTheInitRunAndThePackageManagerSpeaksThroughIt(t *testing.T) {
	t.Parallel()

	deps := initDeps()
	deps.Presentation = func(io.Writer) terminal.Presentation {
		return terminal.Resolve(terminal.Conditions{LogFormat: terminal.FormatJSON, TTY: true, Width: 80})
	}
	deps.RunPackageManager = func(_ context.Context, _ string, _ []string, output io.Writer) error {
		time.Sleep(300 * time.Millisecond)
		fmt.Fprintln(output, "added 1 package in 2s")
		return nil
	}
	var stdout, stderr syncBuffer
	clitest.AttachTerminalSink(deps, &stdout)
	dir := initTestDir(t, "proj")
	if err := os.WriteFile(filepath.Join(dir, "pnpm-lock.yaml"), nil, 0o644); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}

	if err := runInit(context.Background(), deps, dir, "my-app", initOptions{provider: "aws"}); err != nil {
		t.Fatalf("runInit err = %v", err)
	}

	var unit []byte
	var said []string
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		ev := &streamv1.RunEvent{}
		if err := protojson.Unmarshal([]byte(line), ev); err != nil {
			t.Fatalf("line %q is not a protojson RunEvent: %v", line, err)
		}
		switch {
		case ev.GetStarted() != nil && ev.GetSubject() == sdkPackage:
			unit = ev.GetSpanId()
			said = append(said, "started: "+ev.GetMessage())
		case ev.GetOutput() != nil && bytes.Equal(ev.GetSpanId(), unit):
			said = append(said, "output: "+ev.GetMessage())
		case ev.GetEnded() != nil && bytes.Equal(ev.GetSpanId(), unit):
			said = append(said, "ended: "+ev.GetEnded().GetStatus().String())
		}
	}
	want := []string{"started: Adding the SDK to this project with `pnpm add " + sdkPackage + "`", "output: added 1 package in 2s", "ended: SPAN_STATUS_OK"}
	if !slices.Equal(said, want) {
		t.Fatalf("the sdk unit said %q, want %q", said, want)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want the package manager heard only through the run", stderr.String())
	}
}

func initTestDir(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create project dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	return dir
}

func readConfig(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, projectconfig.DefaultFileName))
	if err != nil {
		t.Fatalf("read %s: %v", projectconfig.DefaultFileName, err)
	}
	return string(data)
}

func TestRunInit(t *testing.T) {
	t.Parallel()

	t.Run("no argument defaults the slug to the directory name", func(t *testing.T) {
		t.Parallel()

		deps := initDeps()
		stubPackageManager(&deps, nil)
		dir := initTestDir(t, "My Cool App")

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := runInit(context.Background(), deps, dir, "", initOptions{provider: "aws"}); err != nil {
			t.Fatalf("runInit err = %v; stdout=%s", err, stdout.String())
		}

		content := readConfig(t, dir)
		if !strings.Contains(content, `"slug": "my-cool-app"`) {
			t.Fatalf("config = %q, want slug derived from the directory name", content)
		}
	})

	t.Run("an explicit slug writes a deployable config", func(t *testing.T) {
		t.Parallel()

		deps := initDeps()
		stubPackageManager(&deps, nil)
		dir := initTestDir(t, "ignored-dir-name")

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := runInit(context.Background(), deps, dir, "my-app", initOptions{provider: "aws"}); err != nil {
			t.Fatalf("runInit err = %v; stdout=%s", err, stdout.String())
		}

		content := readConfig(t, dir)
		for _, want := range []string{`"slug": "my-app"`, `"provider": "aws"`, `"$schema"`} {
			if !strings.Contains(content, want) {
				t.Errorf("config = %q, want it to contain %q", content, want)
			}
		}
	})

	t.Run("an invalid slug errors without writing a config", func(t *testing.T) {
		t.Parallel()

		for _, slug := range []string{"My App", "-leading", "trailing-", "under_score", strings.Repeat("a", 64)} {
			t.Run(slug, func(t *testing.T) {
				t.Parallel()

				deps := initDeps()
				argv := stubPackageManager(&deps, nil)
				dir := initTestDir(t, "proj")

				err := runInit(context.Background(), deps, dir, slug, initOptions{provider: "aws"})
				if err == nil {
					t.Fatal("runInit err = nil, want error")
				}
				if _, statErr := os.Stat(filepath.Join(dir, projectconfig.DefaultFileName)); statErr == nil {
					t.Fatal("a config was written for an invalid slug")
				}
				if *argv != nil {
					t.Fatalf("ran %v, want no package manager call", *argv)
				}
			})
		}
	})

	t.Run("an unslugifiable directory name errors asking for a slug", func(t *testing.T) {
		t.Parallel()

		deps := initDeps()
		stubPackageManager(&deps, nil)
		dir := initTestDir(t, "!!!")

		err := runInit(context.Background(), deps, dir, "", initOptions{provider: "aws"})
		if err == nil || !strings.Contains(err.Error(), "ocel init my-app") {
			t.Fatalf("err = %v, want it to ask for a slug", err)
		}
	})

	t.Run("an existing config is never overwritten", func(t *testing.T) {
		t.Parallel()

		deps := initDeps()
		argv := stubPackageManager(&deps, nil)
		dir := initTestDir(t, "proj")
		configPath := filepath.Join(dir, projectconfig.DefaultFileName)
		if err := os.WriteFile(configPath, []byte("existing"), 0o644); err != nil {
			t.Fatalf("write existing config: %v", err)
		}

		err := runInit(context.Background(), deps, dir, "my-app", initOptions{provider: "aws"})
		if err == nil || !strings.Contains(err.Error(), projectconfig.DefaultFileName) {
			t.Fatalf("err = %v, want it to name the config already there", err)
		}
		content, readErr := os.ReadFile(configPath)
		if readErr != nil || string(content) != "existing" {
			t.Fatalf("config = %q (err %v), want the existing file untouched", content, readErr)
		}
		if *argv != nil {
			t.Fatalf("ran %v, want no package manager call", *argv)
		}
	})

	t.Run("--provider names the provider the config is scaffolded with, with options for it to fill in", func(t *testing.T) {
		t.Parallel()

		deps := initDeps()
		stubPackageManager(&deps, nil)
		dir := initTestDir(t, "proj")

		opts := initOptions{provider: "gcp"}
		if err := runInit(context.Background(), deps, dir, "my-app", opts); err != nil {
			t.Fatalf("runInit err = %v", err)
		}

		content := readConfig(t, dir)
		if !strings.Contains(content, `"provider": { "gcp": {} }`) {
			t.Fatalf("config = %q, want the provider asked for and options only the provider knows", content)
		}
	})

	t.Run("no provider is refused, naming the flag, and nothing is written", func(t *testing.T) {
		t.Parallel()

		deps := initDeps()
		argv := stubPackageManager(&deps, nil)
		dir := initTestDir(t, "proj")

		err := runInit(context.Background(), deps, dir, "my-app", initOptions{})
		if err == nil || !strings.Contains(err.Error(), "--provider") {
			t.Fatalf("err = %v, want it to ask for --provider", err)
		}
		if !strings.Contains(err.Error(), "aws, gcp, vps") {
			t.Fatalf("err = %v, want it to name the providers ocel ships", err)
		}
		if _, statErr := os.Stat(filepath.Join(dir, projectconfig.DefaultFileName)); statErr == nil {
			t.Fatal("a config was written with no provider named")
		}
		if *argv != nil {
			t.Fatalf("ran %v, want no package manager call", *argv)
		}
	})

	t.Run("it adds the SDK with the package manager the lockfile names", func(t *testing.T) {
		t.Parallel()

		lockfiles := map[string][]string{
			"pnpm-lock.yaml":    {"pnpm", "add", sdkPackage},
			"yarn.lock":         {"yarn", "add", sdkPackage},
			"bun.lockb":         {"bun", "add", sdkPackage},
			"package-lock.json": {"npm", "install", sdkPackage},
		}
		for name, want := range lockfiles {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				deps := initDeps()
				argv := stubPackageManager(&deps, nil)
				dir := initTestDir(t, "proj")
				if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
					t.Fatalf("write lockfile: %v", err)
				}

				if err := runInit(context.Background(), deps, dir, "my-app", initOptions{provider: "aws"}); err != nil {
					t.Fatalf("runInit err = %v", err)
				}
				if got := *argv; !slices.Equal(got, want) {
					t.Fatalf("ran %v, want %v", got, want)
				}
			})
		}
	})

	t.Run("a failing package manager keeps the config and prints the command", func(t *testing.T) {
		t.Parallel()

		deps := initDeps()
		stubPackageManager(&deps, errors.New("exec: \"pnpm\": executable file not found in $PATH"))
		dir := initTestDir(t, "proj")
		if err := os.WriteFile(filepath.Join(dir, "pnpm-lock.yaml"), nil, 0o644); err != nil {
			t.Fatalf("write lockfile: %v", err)
		}

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := runInit(context.Background(), deps, dir, "my-app", initOptions{provider: "aws"}); err != nil {
			t.Fatalf("runInit err = %v, want the failed install to be non-fatal", err)
		}
		if !strings.Contains(readConfig(t, dir), `"slug": "my-app"`) {
			t.Fatal("config should still have been written")
		}
		if !strings.Contains(stdout.String(), "pnpm add "+sdkPackage) {
			t.Fatalf("stdout = %q, want the command the user should run", stdout.String())
		}
	})

	t.Run("--config writes the config where the path points", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		cwd := filepath.Join(root, "app")
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatalf("create cwd: %v", err)
		}
		project := filepath.Join(root, "project")
		if err := os.MkdirAll(project, 0o755); err != nil {
			t.Fatalf("create project dir: %v", err)
		}
		for _, name := range []string{"package.json", "pnpm-lock.yaml"} {
			if err := os.WriteFile(filepath.Join(project, name), []byte("{}\n"), 0o644); err != nil {
				t.Fatalf("write %s: %v", name, err)
			}
		}

		deps := initDeps()
		argv := stubPackageManager(&deps, nil)
		opts := initOptions{provider: "aws", configPath: filepath.Join("..", "project", projectconfig.DefaultFileName)}

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := runInit(context.Background(), deps, cwd, "", opts); err != nil {
			t.Fatalf("runInit err = %v; stdout=%s", err, stdout.String())
		}

		content, err := os.ReadFile(filepath.Join(project, projectconfig.DefaultFileName))
		if err != nil {
			t.Fatalf("read the config --config named: %v", err)
		}
		if !strings.Contains(string(content), `"slug": "project"`) {
			t.Errorf("config = %q, want the slug derived from the config's own directory", content)
		}
		if got := *argv; !slices.Equal(got, []string{"pnpm", "add", sdkPackage}) {
			t.Errorf("ran %v, want the sdk added beside the config, not beside the working directory", got)
		}
		if !strings.Contains(stdout.String(), "Wrote "+projectconfig.DefaultFileName+" for project ") {
			t.Errorf("stdout = %q, want it to name the config written", stdout.String())
		}
	})

	t.Run("--config creates the directories leading to the path", func(t *testing.T) {
		t.Parallel()

		deps := initDeps()
		stubPackageManager(&deps, nil)
		dir := initTestDir(t, "proj")

		opts := initOptions{provider: "aws", configPath: filepath.Join("nested", "deep", projectconfig.DefaultFileName)}
		if err := runInit(context.Background(), deps, dir, "my-app", opts); err != nil {
			t.Fatalf("runInit err = %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "nested", "deep", projectconfig.DefaultFileName)); err != nil {
			t.Fatalf("stat the config --config named: %v", err)
		}
	})
}

func TestProviderIdentifier(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"aws":        "awsProvider",
		"gcp":        "gcpProvider",
		"bare-metal": "bareMetalProvider",
		"123":        "provider",
	}
	for name, want := range cases {
		if got := providerIdentifier(name); got != want {
			t.Errorf("providerIdentifier(%q) = %q, want %q", name, got, want)
		}
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
