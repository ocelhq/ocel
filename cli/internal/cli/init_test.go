package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func stubPackageManager(deps *cmddeps.Deps, result error) *[]string {
	var argv []string
	deps.RunPackageManager = func(_ context.Context, _ string, cmd []string, _ io.Writer) error {
		argv = cmd
		return result
	}
	return &argv
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

		deps := newDeps()
		stubPackageManager(&deps, nil)
		dir := initTestDir(t, "My Cool App")

		var stdout bytes.Buffer
		if err := runInit(context.Background(), deps, dir, "", initOptions{provider: "acme"}, &stdout, &bytes.Buffer{}); err != nil {
			t.Fatalf("runInit err = %v; stdout=%s", err, stdout.String())
		}

		content := readConfig(t, dir)
		if !strings.Contains(content, `"slug": "my-cool-app"`) {
			t.Fatalf("config = %q, want slug derived from the directory name", content)
		}
	})

	t.Run("an explicit slug writes a deployable config", func(t *testing.T) {
		t.Parallel()

		deps := newDeps()
		stubPackageManager(&deps, nil)
		dir := initTestDir(t, "ignored-dir-name")

		var stdout bytes.Buffer
		if err := runInit(context.Background(), deps, dir, "my-app", initOptions{provider: "acme"}, &stdout, &bytes.Buffer{}); err != nil {
			t.Fatalf("runInit err = %v; stdout=%s", err, stdout.String())
		}

		content := readConfig(t, dir)
		for _, want := range []string{`"slug": "my-app"`, `"provider": { "name": "acme", "options": {} }`, `"$schema"`} {
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

				deps := newDeps()
				argv := stubPackageManager(&deps, nil)
				dir := initTestDir(t, "proj")

				err := runInit(context.Background(), deps, dir, slug, initOptions{provider: "acme"}, &bytes.Buffer{}, &bytes.Buffer{})
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

		deps := newDeps()
		stubPackageManager(&deps, nil)
		dir := initTestDir(t, "!!!")

		err := runInit(context.Background(), deps, dir, "", initOptions{provider: "acme"}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "ocel init my-app") {
			t.Fatalf("err = %v, want it to ask for a slug", err)
		}
	})

	t.Run("an existing config is never overwritten", func(t *testing.T) {
		t.Parallel()

		deps := newDeps()
		argv := stubPackageManager(&deps, nil)
		dir := initTestDir(t, "proj")
		configPath := filepath.Join(dir, projectconfig.DefaultFileName)
		if err := os.WriteFile(configPath, []byte("existing"), 0o644); err != nil {
			t.Fatalf("write existing config: %v", err)
		}

		err := runInit(context.Background(), deps, dir, "my-app", initOptions{provider: "acme"}, &bytes.Buffer{}, &bytes.Buffer{})
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

		deps := newDeps()
		stubPackageManager(&deps, nil)
		dir := initTestDir(t, "proj")

		opts := initOptions{provider: "gcp"}
		if err := runInit(context.Background(), deps, dir, "my-app", opts, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatalf("runInit err = %v", err)
		}

		content := readConfig(t, dir)
		if !strings.Contains(content, `"provider": { "name": "gcp", "options": {} }`) {
			t.Fatalf("config = %q, want the provider asked for and options only the provider knows", content)
		}
	})

	t.Run("no provider is refused, naming the flag, and nothing is written", func(t *testing.T) {
		t.Parallel()

		deps := newDeps()
		argv := stubPackageManager(&deps, nil)
		dir := initTestDir(t, "proj")

		err := runInit(context.Background(), deps, dir, "my-app", initOptions{}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "--provider") {
			t.Fatalf("err = %v, want it to ask for --provider", err)
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

				deps := newDeps()
				argv := stubPackageManager(&deps, nil)
				dir := initTestDir(t, "proj")
				if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
					t.Fatalf("write lockfile: %v", err)
				}

				if err := runInit(context.Background(), deps, dir, "my-app", initOptions{provider: "acme"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
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

		deps := newDeps()
		stubPackageManager(&deps, errors.New("exec: \"pnpm\": executable file not found in $PATH"))
		dir := initTestDir(t, "proj")
		if err := os.WriteFile(filepath.Join(dir, "pnpm-lock.yaml"), nil, 0o644); err != nil {
			t.Fatalf("write lockfile: %v", err)
		}

		var stdout bytes.Buffer
		if err := runInit(context.Background(), deps, dir, "my-app", initOptions{provider: "acme"}, &stdout, &bytes.Buffer{}); err != nil {
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

		deps := newDeps()
		argv := stubPackageManager(&deps, nil)
		opts := initOptions{provider: "acme", configPath: filepath.Join("..", "project", projectconfig.DefaultFileName)}

		var stdout bytes.Buffer
		if err := runInit(context.Background(), deps, cwd, "", opts, &stdout, &bytes.Buffer{}); err != nil {
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
		if !strings.Contains(stdout.String(), "Wrote "+projectconfig.DefaultFileName) {
			t.Errorf("stdout = %q, want it to name the config written", stdout.String())
		}
	})

	t.Run("--config creates the directories leading to the path", func(t *testing.T) {
		t.Parallel()

		deps := newDeps()
		stubPackageManager(&deps, nil)
		dir := initTestDir(t, "proj")

		opts := initOptions{provider: "acme", configPath: filepath.Join("nested", "deep", projectconfig.DefaultFileName)}
		if err := runInit(context.Background(), deps, dir, "my-app", opts, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
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
