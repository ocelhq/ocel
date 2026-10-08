package projectinit

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

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/pkg/configdoc"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

func stubPackageManager(dependencies *Dependencies, result error) *[]string {
	var argv []string
	dependencies.RunPackageManager = func(_ context.Context, _ string, cmd []string, _ io.Writer) error {
		argv = cmd
		return result
	}
	return &argv
}

func TestAddingTheSDKIsASpanOnTheInitRunAndThePackageManagerSpeaksThroughIt(t *testing.T) {
	t.Parallel()

	dependencies := newTestDependencies()
	dependencies.Presentation = func(io.Writer) terminal.Presentation {
		return terminal.Resolve(terminal.Conditions{Format: terminal.FormatJSON, TTY: true, Width: 80})
	}
	dependencies.RunPackageManager = func(_ context.Context, _ string, _ []string, output io.Writer) error {
		time.Sleep(300 * time.Millisecond)
		fmt.Fprintln(output, "added 1 package in 2s")
		return nil
	}
	var stdout, stderr syncBuffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	dir := initTestDir(t, "proj")
	if err := os.WriteFile(filepath.Join(dir, "pnpm-lock.yaml"), nil, 0o644); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}

	if _, err := runInit(context.Background(), dependencies, dir, "my-app", initOptions{provider: "fake"}); err != nil {
		t.Fatalf("runInit err = %v", err)
	}

	var span []byte
	var said []string
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		ev := &streamv1.RunEvent{}
		if err := protojson.Unmarshal([]byte(line), ev); err != nil {
			t.Fatalf("line %q is not a protojson RunEvent: %v", line, err)
		}
		switch {
		case ev.GetOperation().GetStarted() != nil && ev.GetOperation().GetSubject() == sdkPackage:
			span = ev.GetOperation().GetSpanId()
			said = append(said, "started: "+ev.GetOperation().GetMessage())
		case ev.GetOperation().GetOutput() != nil && bytes.Equal(ev.GetOperation().GetSpanId(), span):
			said = append(said, "output: "+ev.GetOperation().GetMessage())
		case ev.GetOperation().GetEnded() != nil && bytes.Equal(ev.GetOperation().GetSpanId(), span):
			said = append(said, "ended: "+ev.GetOperation().GetEnded().GetStatus().String())
		}
	}
	want := []string{"started: Adding the SDK to this project with `pnpm add " + sdkPackage + "`", "output: added 1 package in 2s", "ended: SPAN_STATUS_OK"}
	if !slices.Equal(said, want) {
		t.Fatalf("the sdk span said %q, want %q", said, want)
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
	data, err := os.ReadFile(filepath.Join(dir, project.TSFileName))
	if err != nil {
		t.Fatalf("read %s: %v", project.TSFileName, err)
	}
	return string(data)
}

func TestInitWritesADeployableConfigForTheSlugAndProviderItIsGiven(t *testing.T) {
	t.Parallel()

	t.Run("no argument defaults the slug to the directory name", func(t *testing.T) {
		t.Parallel()

		dependencies := newTestDependencies()
		stubPackageManager(&dependencies, nil)
		dir := initTestDir(t, "My Cool App")

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if _, err := runInit(context.Background(), dependencies, dir, "", initOptions{provider: "fake"}); err != nil {
			t.Fatalf("runInit err = %v; stdout=%s", err, stdout.String())
		}

		content := readConfig(t, dir)
		if !strings.Contains(content, `slug: "my-cool-app"`) {
			t.Fatalf("config = %q, want slug derived from the directory name", content)
		}
	})

	t.Run("an explicit slug writes a deployable config", func(t *testing.T) {
		t.Parallel()

		dependencies := newTestDependencies()
		stubPackageManager(&dependencies, nil)
		dir := initTestDir(t, "ignored-dir-name")

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if _, err := runInit(context.Background(), dependencies, dir, "my-app", initOptions{provider: "fake"}); err != nil {
			t.Fatalf("runInit err = %v; stdout=%s", err, stdout.String())
		}

		content := readConfig(t, dir)
		for _, want := range []string{`from "ocel/config"`, `slug: "my-app"`, `provider: fakeProvider({})`} {
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

				dependencies := newTestDependencies()
				argv := stubPackageManager(&dependencies, nil)
				dir := initTestDir(t, "proj")

				_, err := runInit(context.Background(), dependencies, dir, slug, initOptions{provider: "fake"})
				if err == nil {
					t.Fatal("runInit err = nil, want error")
				}
				if _, statErr := os.Stat(filepath.Join(dir, project.TSFileName)); statErr == nil {
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

		dependencies := newTestDependencies()
		stubPackageManager(&dependencies, nil)
		dir := initTestDir(t, "!!!")

		_, err := runInit(context.Background(), dependencies, dir, "", initOptions{provider: "fake"})
		if err == nil || !strings.Contains(err.Error(), "ocel init my-app") {
			t.Fatalf("err = %v, want it to ask for a slug", err)
		}
	})

	t.Run("an existing config is never overwritten", func(t *testing.T) {
		t.Parallel()

		dependencies := newTestDependencies()
		argv := stubPackageManager(&dependencies, nil)
		dir := initTestDir(t, "proj")
		configPath := filepath.Join(dir, project.DefaultFileName)
		if err := os.WriteFile(configPath, []byte("existing"), 0o644); err != nil {
			t.Fatalf("write existing config: %v", err)
		}

		_, err := runInit(context.Background(), dependencies, dir, "my-app", initOptions{provider: "fake"})
		if err == nil || !strings.Contains(err.Error(), project.DefaultFileName) {
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

		dependencies := newTestDependencies()
		stubPackageManager(&dependencies, nil)
		dir := initTestDir(t, "proj")

		keyed := providerKeyed()
		opts := initOptions{provider: keyed}
		if _, err := runInit(context.Background(), dependencies, dir, "my-app", opts); err != nil {
			t.Fatalf("runInit err = %v", err)
		}

		content := readConfig(t, dir)
		if !strings.Contains(content, fmt.Sprintf(`provider: %s({})`, providerIdentifier(keyed))) {
			t.Fatalf("config = %q, want the provider asked for and options only the provider knows", content)
		}
	})

	t.Run("no provider is refused with input_required, naming the flag, and nothing is written", func(t *testing.T) {
		t.Parallel()

		dependencies := newTestDependencies()
		argv := stubPackageManager(&dependencies, nil)
		dir := initTestDir(t, "proj")

		_, err := runInit(context.Background(), dependencies, dir, "my-app", initOptions{})
		if err == nil || !strings.Contains(err.Error(), "--provider") {
			t.Fatalf("err = %v, want it to ask for --provider", err)
		}
		if !strings.Contains(err.Error(), strings.Join(configdoc.ProviderIDs(), ", ")) {
			t.Fatalf("err = %v, want it to name the providers ocel ships", err)
		}
		if got := clierror.NewRunError(err); got.GetCode() != clierror.CodeInputRequired || got.GetHint() != "--provider <id>" {
			t.Errorf("run error = %v, want input_required with the hint --provider <id>", got)
		}
		if _, statErr := os.Stat(filepath.Join(dir, project.TSFileName)); statErr == nil {
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

				dependencies := newTestDependencies()
				argv := stubPackageManager(&dependencies, nil)
				dir := initTestDir(t, "proj")
				if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
					t.Fatalf("write lockfile: %v", err)
				}

				if _, err := runInit(context.Background(), dependencies, dir, "my-app", initOptions{provider: "fake"}); err != nil {
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

		dependencies := newTestDependencies()
		stubPackageManager(&dependencies, errors.New("exec: \"pnpm\": executable file not found in $PATH"))
		dir := initTestDir(t, "proj")
		if err := os.WriteFile(filepath.Join(dir, "pnpm-lock.yaml"), nil, 0o644); err != nil {
			t.Fatalf("write lockfile: %v", err)
		}

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if _, err := runInit(context.Background(), dependencies, dir, "my-app", initOptions{provider: "fake"}); err != nil {
			t.Fatalf("runInit err = %v, want the failed install to be non-fatal", err)
		}
		if !strings.Contains(readConfig(t, dir), `slug: "my-app"`) {
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
		projectDir := filepath.Join(root, "project")
		if err := os.MkdirAll(projectDir, 0o755); err != nil {
			t.Fatalf("create project dir: %v", err)
		}
		for _, name := range []string{"package.json", "pnpm-lock.yaml"} {
			if err := os.WriteFile(filepath.Join(projectDir, name), []byte("{}\n"), 0o644); err != nil {
				t.Fatalf("write %s: %v", name, err)
			}
		}

		dependencies := newTestDependencies()
		argv := stubPackageManager(&dependencies, nil)
		opts := initOptions{provider: "fake", configPath: filepath.Join("..", "project", project.DefaultFileName)}

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if _, err := runInit(context.Background(), dependencies, cwd, "", opts); err != nil {
			t.Fatalf("runInit err = %v; stdout=%s", err, stdout.String())
		}

		content, err := os.ReadFile(filepath.Join(projectDir, project.DefaultFileName))
		if err != nil {
			t.Fatalf("read the config --config named: %v", err)
		}
		if !strings.Contains(string(content), `"slug": "project"`) {
			t.Errorf("config = %q, want the slug derived from the config's own directory", content)
		}
		if got := *argv; !slices.Equal(got, []string{"pnpm", "add", sdkPackage}) {
			t.Errorf("ran %v, want the sdk added beside the config, not beside the working directory", got)
		}
		if !strings.Contains(stdout.String(), "Wrote "+project.DefaultFileName+" for project ") {
			t.Errorf("stdout = %q, want it to name the config written", stdout.String())
		}
	})

	t.Run("--config creates the directories leading to the path", func(t *testing.T) {
		t.Parallel()

		dependencies := newTestDependencies()
		stubPackageManager(&dependencies, nil)
		dir := initTestDir(t, "proj")

		opts := initOptions{provider: "fake", configPath: filepath.Join("nested", "deep", project.DefaultFileName)}
		if _, err := runInit(context.Background(), dependencies, dir, "my-app", opts); err != nil {
			t.Fatalf("runInit err = %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "nested", "deep", project.DefaultFileName)); err != nil {
			t.Fatalf("stat the config --config named: %v", err)
		}
	})
}

func TestAProviderIDBecomesACamelCaseIdentifierInTheTypeScriptConfig(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"fake":       "fakeProvider",
		"second":     "secondProvider",
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

func TestInitAsJSONPrintsWhatItWrote(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		opts       initOptions
		wantFile   string
		wantFormat resultv1.ConfigFormat
	}{
		"typescript": {initOptions{provider: "fake"}, project.TSFileName, resultv1.ConfigFormat_CONFIG_FORMAT_TYPESCRIPT},
		"json":       {initOptions{provider: "fake", format: "json"}, project.DefaultFileName, resultv1.ConfigFormat_CONFIG_FORMAT_JSON},
		"yaml":       {initOptions{provider: "fake", format: "yaml"}, project.YAMLFileName, resultv1.ConfigFormat_CONFIG_FORMAT_YAML},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dependencies := newTestDependencies()
			dependencies.Presentation = clitest.ResolveJSONPresentation
			argv := stubPackageManager(&dependencies, nil)
			dir := initTestDir(t, "proj")

			var stdout, stderr bytes.Buffer
			clitest.AttachTerminalSink(dependencies.Invocation, &stderr)
			if err := runInitCommand(context.Background(), dependencies, dir, "my-app", tc.opts, &stdout); err != nil {
				t.Fatalf("runInitCommand err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}

			var got resultv1.InitResult
			clitest.DecodeResultInto(t, stdout.String(), &got)
			if got.GetConfigPath() != filepath.Join(dir, tc.wantFile) || got.GetFormat() != tc.wantFormat {
				t.Errorf("init result = %v, want %s written as %v", &got, filepath.Join(dir, tc.wantFile), tc.wantFormat)
			}
			if got.GetSlug() != "my-app" || got.GetProvider() != "fake" || got.GetLanguage() != "node" {
				t.Errorf("init result = %v, want the slug, provider and language it set up", &got)
			}
			if got.GetSdkPackage() != sdkPackage || !slices.Contains(*argv, sdkPackage) {
				t.Errorf("sdk_package = %q after running %q, want %q", got.GetSdkPackage(), *argv, sdkPackage)
			}
			if len(clitest.RunEvents(t, stderr.String())) == 0 {
				t.Errorf("stream = %q, want the run's events there", stderr.String())
			}
		})
	}
}

func TestInitAsJSONNamesNoSDKPackageWhenNoneWasAdded(t *testing.T) {
	t.Parallel()

	t.Run("the package manager failed", func(t *testing.T) {
		t.Parallel()

		dependencies := newTestDependencies()
		dependencies.Presentation = clitest.ResolveJSONPresentation
		stubPackageManager(&dependencies, errors.New("offline"))
		dir := initTestDir(t, "proj")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stderr)
		if err := runInitCommand(context.Background(), dependencies, dir, "my-app", initOptions{provider: "fake"}, &stdout); err != nil {
			t.Fatalf("runInitCommand err = %v", err)
		}

		data := clitest.DecodeResult(t, stdout.String())
		if _, present := data["sdkPackage"]; present {
			t.Errorf("init json = %v, want no sdkPackage", data)
		}
	})

	t.Run("no manifest says which SDK to add", func(t *testing.T) {
		t.Parallel()

		dependencies := newTestDependencies()
		dependencies.Presentation = clitest.ResolveJSONPresentation
		stubPackageManager(&dependencies, nil)
		dir := filepath.Join(t.TempDir(), "bare")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stderr)
		if err := runInitCommand(context.Background(), dependencies, dir, "my-app", initOptions{provider: "fake"}, &stdout); err != nil {
			t.Fatalf("runInitCommand err = %v", err)
		}

		data := clitest.DecodeResult(t, stdout.String())
		if _, present := data["sdkPackage"]; present || data["language"] != "" {
			t.Errorf("init json = %v, want no language and no sdkPackage", data)
		}
	})
}

func TestInitAsJSONNamesTheSDKPackageOfTheLanguageItAdded(t *testing.T) {
	t.Parallel()

	for lang, want := range map[string]string{"go": goSDKModule, "rust": rustSDKCrate, "python": sdkPackage} {
		t.Run(lang, func(t *testing.T) {
			t.Parallel()

			dependencies := newTestDependencies()
			dependencies.Presentation = clitest.ResolveJSONPresentation
			stubPackageManager(&dependencies, nil)
			dir := initTestDir(t, "proj")

			var stdout, stderr bytes.Buffer
			clitest.AttachTerminalSink(dependencies.Invocation, &stderr)
			if err := runInitCommand(context.Background(), dependencies, dir, "my-app", initOptions{provider: "fake", language: lang}, &stdout); err != nil {
				t.Fatalf("runInitCommand err = %v", err)
			}

			var got resultv1.InitResult
			clitest.DecodeResultInto(t, stdout.String(), &got)
			if got.GetLanguage() != lang || got.GetSdkPackage() != want {
				t.Errorf("init result = %v, want %s with the %s package", &got, lang, want)
			}
		})
	}
}

func TestInitInHumanModePrintsNoResult(t *testing.T) {
	t.Parallel()

	dependencies := newTestDependencies()
	stubPackageManager(&dependencies, nil)
	dir := initTestDir(t, "proj")

	var stdout, stream bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stream)
	if err := runInitCommand(context.Background(), dependencies, dir, "my-app", initOptions{provider: "fake"}, &stdout); err != nil {
		t.Fatalf("runInitCommand err = %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want the run's own output to be all a person sees", stdout.String())
	}
}
