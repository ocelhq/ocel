package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/session"
)

func stubPackageManager(sess *session.Session, result error) *[]string {
	var argv []string
	sess.RunPackageManager = func(_ context.Context, _ string, cmd []string, _ io.Writer) error {
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
	data, err := os.ReadFile(filepath.Join(dir, "ocel.config.ts"))
	if err != nil {
		t.Fatalf("read ocel.config.ts: %v", err)
	}
	return string(data)
}

func TestRunInit(t *testing.T) {
	t.Parallel()

	t.Run("no argument defaults the slug to the directory name", func(t *testing.T) {
		t.Parallel()

		d := newSession()
		stubPackageManager(&d, nil)
		dir := initTestDir(t, "My Cool App")

		var stdout bytes.Buffer
		if err := runInit(context.Background(), d, dir, "", initOptions{}, &stdout, &bytes.Buffer{}); err != nil {
			t.Fatalf("runInit err = %v; stdout=%s", err, stdout.String())
		}

		content := readConfig(t, dir)
		if !strings.Contains(content, `slug: "my-cool-app"`) {
			t.Fatalf("config = %q, want slug derived from the directory name", content)
		}
	})

	t.Run("an explicit slug writes a deployable config", func(t *testing.T) {
		t.Parallel()

		d := newSession()
		stubPackageManager(&d, nil)
		dir := initTestDir(t, "ignored-dir-name")

		var stdout bytes.Buffer
		if err := runInit(context.Background(), d, dir, "my-app", initOptions{}, &stdout, &bytes.Buffer{}); err != nil {
			t.Fatalf("runInit err = %v; stdout=%s", err, stdout.String())
		}

		content := readConfig(t, dir)
		for _, want := range []string{
			`import { defineConfig } from "ocel/config";`,
			`import awsProvider from "@ocel/provider-aws";`,
			`slug: "my-app"`,
			`provider: awsProvider()`,
		} {
			if !strings.Contains(content, want) {
				t.Errorf("config = %q, want it to contain %q", content, want)
			}
		}
		if strings.Contains(content, "projectId") {
			t.Errorf("config = %q, want no projectId", content)
		}
	})

	t.Run("an invalid slug errors without writing a config", func(t *testing.T) {
		t.Parallel()

		for _, slug := range []string{"My App", "-leading", "trailing-", "under_score", strings.Repeat("a", 64)} {
			t.Run(slug, func(t *testing.T) {
				t.Parallel()

				d := newSession()
				stubPackageManager(&d, nil)
				dir := initTestDir(t, "proj")

				err := runInit(context.Background(), d, dir, slug, initOptions{}, &bytes.Buffer{}, &bytes.Buffer{})
				if err == nil {
					t.Fatal("runInit err = nil, want error")
				}
				if !strings.Contains(err.Error(), "invalid slug") {
					t.Fatalf("err = %v, want it to name the slug as invalid", err)
				}
				if _, statErr := os.Stat(filepath.Join(dir, "ocel.config.ts")); !errors.Is(statErr, fs.ErrNotExist) {
					t.Fatal("ocel.config.ts should not have been written")
				}
			})
		}
	})

	t.Run("an unslugifiable directory name errors asking for a slug", func(t *testing.T) {
		t.Parallel()

		d := newSession()
		stubPackageManager(&d, nil)
		dir := initTestDir(t, "!!!")

		err := runInit(context.Background(), d, dir, "", initOptions{}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil {
			t.Fatal("runInit err = nil, want error")
		}
		if !strings.Contains(err.Error(), "ocel init my-app") {
			t.Fatalf("err = %v, want it to show how to pass a slug", err)
		}
	})

	t.Run("an existing config is never overwritten", func(t *testing.T) {
		t.Parallel()

		d := newSession()
		argv := stubPackageManager(&d, nil)
		dir := initTestDir(t, "proj")
		configPath := filepath.Join(dir, "ocel.config.ts")
		if err := os.WriteFile(configPath, []byte("existing"), 0o644); err != nil {
			t.Fatalf("write existing config: %v", err)
		}

		err := runInit(context.Background(), d, dir, "my-app", initOptions{}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil {
			t.Fatal("runInit err = nil, want error")
		}
		if !strings.Contains(err.Error(), "ocel.config.ts") {
			t.Fatalf("err = %v, want it to mention ocel.config.ts", err)
		}
		if content := readConfig(t, dir); content != "existing" {
			t.Fatalf("config = %q, want the existing file untouched", content)
		}
		if *argv != nil {
			t.Fatalf("ran %v, want no package manager call", *argv)
		}
	})

	t.Run("--provider overrides the default package", func(t *testing.T) {
		t.Parallel()

		d := newSession()
		argv := stubPackageManager(&d, nil)
		dir := initTestDir(t, "proj")

		opts := initOptions{provider: "@acme/provider-gcp"}
		if err := runInit(context.Background(), d, dir, "my-app", opts, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatalf("runInit err = %v", err)
		}

		content := readConfig(t, dir)
		if !strings.Contains(content, `import gcpProvider from "@acme/provider-gcp";`) || !strings.Contains(content, "provider: gcpProvider()") {
			t.Fatalf("config = %q, want it to use the overridden provider", content)
		}
		if got := *argv; !slices.Equal(got, []string{"npm", "install", sdkPackage, "@acme/provider-gcp"}) {
			t.Fatalf("ran %v, want the overridden package added alongside %s", got, sdkPackage)
		}
	})

	t.Run("it installs the SDK alongside the provider", func(t *testing.T) {
		t.Parallel()

		d := newSession()
		argv := stubPackageManager(&d, nil)
		dir := initTestDir(t, "proj")

		var stdout bytes.Buffer
		if err := runInit(context.Background(), d, dir, "my-app", initOptions{}, &stdout, &bytes.Buffer{}); err != nil {
			t.Fatalf("runInit err = %v", err)
		}

		want := []string{"npm", "install", sdkPackage, defaultProviderPackage}
		if got := *argv; !slices.Equal(got, want) {
			t.Fatalf("ran %v, want %v", got, want)
		}
		if !strings.Contains(readConfig(t, dir), `from "`+sdkPackage+`/config"`) {
			t.Fatalf("config = %q, want it to import from %s/config", readConfig(t, dir), sdkPackage)
		}
	})

	t.Run("it adds the provider with the package manager the lockfile names", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			lockfile string
			want     []string
		}{
			{"pnpm-lock.yaml", []string{"pnpm", "add", sdkPackage, defaultProviderPackage}},
			{"yarn.lock", []string{"yarn", "add", sdkPackage, defaultProviderPackage}},
			{"bun.lockb", []string{"bun", "add", sdkPackage, defaultProviderPackage}},
			{"package-lock.json", []string{"npm", "install", sdkPackage, defaultProviderPackage}},
			{"", []string{"npm", "install", sdkPackage, defaultProviderPackage}},
		}
		for _, tc := range cases {
			name := tc.lockfile
			if name == "" {
				name = "no lockfile falls back to npm"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				d := newSession()
				argv := stubPackageManager(&d, nil)
				dir := initTestDir(t, "proj")
				if tc.lockfile != "" {
					if err := os.WriteFile(filepath.Join(dir, tc.lockfile), nil, 0o644); err != nil {
						t.Fatalf("write lockfile: %v", err)
					}
				}

				var stdout bytes.Buffer
				if err := runInit(context.Background(), d, dir, "my-app", initOptions{}, &stdout, &bytes.Buffer{}); err != nil {
					t.Fatalf("runInit err = %v", err)
				}
				if got := *argv; !slices.Equal(got, tc.want) {
					t.Fatalf("ran %v, want %v", got, tc.want)
				}
				if !strings.Contains(stdout.String(), "Added "+sdkPackage+" "+defaultProviderPackage) {
					t.Fatalf("stdout = %q, want it to report the added packages", stdout.String())
				}
			})
		}
	})

	t.Run("with no package.json it skips the install and says so", func(t *testing.T) {
		t.Parallel()

		d := newSession()
		argv := stubPackageManager(&d, nil)
		dir := initTestDir(t, "proj")
		if err := os.Remove(filepath.Join(dir, "package.json")); err != nil {
			t.Fatalf("remove package.json: %v", err)
		}

		var stdout bytes.Buffer
		if err := runInit(context.Background(), d, dir, "my-app", initOptions{}, &stdout, &bytes.Buffer{}); err != nil {
			t.Fatalf("runInit err = %v", err)
		}
		if *argv != nil {
			t.Fatalf("ran %v, want no package manager call", *argv)
		}
		if !strings.Contains(stdout.String(), "npm install "+sdkPackage+" "+defaultProviderPackage) {
			t.Fatalf("stdout = %q, want the command to run later", stdout.String())
		}
	})

	t.Run("a failing package manager keeps the config and prints the command", func(t *testing.T) {
		t.Parallel()

		d := newSession()
		stubPackageManager(&d, errors.New("exec: \"pnpm\": executable file not found in $PATH"))
		dir := initTestDir(t, "proj")
		if err := os.WriteFile(filepath.Join(dir, "pnpm-lock.yaml"), nil, 0o644); err != nil {
			t.Fatalf("write lockfile: %v", err)
		}

		var stdout bytes.Buffer
		if err := runInit(context.Background(), d, dir, "my-app", initOptions{}, &stdout, &bytes.Buffer{}); err != nil {
			t.Fatalf("runInit err = %v, want the failed install to be non-fatal", err)
		}
		if !strings.Contains(readConfig(t, dir), `slug: "my-app"`) {
			t.Fatal("config should still have been written")
		}
		if !strings.Contains(stdout.String(), "pnpm add "+sdkPackage+" "+defaultProviderPackage) {
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
		infra := filepath.Join(root, "infra")
		if err := os.MkdirAll(infra, 0o755); err != nil {
			t.Fatalf("create infra dir: %v", err)
		}
		for _, name := range []string{"package.json", "pnpm-lock.yaml"} {
			if err := os.WriteFile(filepath.Join(infra, name), []byte("{}\n"), 0o644); err != nil {
				t.Fatalf("write %s: %v", name, err)
			}
		}

		d := newSession()
		argv := stubPackageManager(&d, nil)
		opts := initOptions{configPath: filepath.Join("..", "infra", "ocel.ts")}

		var stdout bytes.Buffer
		if err := runInit(context.Background(), d, cwd, "", opts, &stdout, &bytes.Buffer{}); err != nil {
			t.Fatalf("runInit err = %v; stdout=%s", err, stdout.String())
		}

		content, err := os.ReadFile(filepath.Join(infra, "ocel.ts"))
		if err != nil {
			t.Fatalf("read the config --config named: %v", err)
		}
		if !strings.Contains(string(content), `slug: "infra"`) {
			t.Errorf("config = %q, want the slug derived from the config's own directory", content)
		}
		if got := *argv; !slices.Equal(got, []string{"pnpm", "add", sdkPackage, defaultProviderPackage}) {
			t.Errorf("ran %v, want the dependencies added beside the config, not beside the working directory", got)
		}
		if !strings.Contains(stdout.String(), "Wrote ocel.ts") {
			t.Errorf("stdout = %q, want it to name the config written", stdout.String())
		}
	})

	t.Run("--config creates the directories leading to the path", func(t *testing.T) {
		t.Parallel()

		d := newSession()
		stubPackageManager(&d, nil)
		dir := initTestDir(t, "proj")

		opts := initOptions{configPath: filepath.Join("nested", "deep", "ocel.ts")}
		if err := runInit(context.Background(), d, dir, "my-app", opts, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatalf("runInit err = %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "nested", "deep", "ocel.ts")); err != nil {
			t.Fatalf("stat the config --config named: %v", err)
		}
	})

	t.Run("--config never overwrites an existing config", func(t *testing.T) {
		t.Parallel()

		d := newSession()
		argv := stubPackageManager(&d, nil)
		dir := initTestDir(t, "proj")
		configPath := filepath.Join(dir, "ocel.ts")
		if err := os.WriteFile(configPath, []byte("existing"), 0o644); err != nil {
			t.Fatalf("write existing config: %v", err)
		}

		err := runInit(context.Background(), d, dir, "my-app", initOptions{configPath: "ocel.ts"}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil {
			t.Fatal("runInit err = nil, want error")
		}
		if !strings.Contains(err.Error(), "ocel.ts") {
			t.Fatalf("err = %v, want it to mention ocel.ts", err)
		}
		content, readErr := os.ReadFile(configPath)
		if readErr != nil || string(content) != "existing" {
			t.Fatalf("config = %q (err %v), want the existing file untouched", content, readErr)
		}
		if *argv != nil {
			t.Fatalf("ran %v, want no package manager call", *argv)
		}
	})
}

func TestProviderIdentifier(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"@ocel/provider-aws":        "awsProvider",
		"@acme/provider-gcp":        "gcpProvider",
		"provider-aws":              "awsProvider",
		"@acme/provider-bare-metal": "bareMetalProvider",
		"@acme/whatever":            "whateverProvider",
		"@acme/provider-123":        "provider",
	}
	for pkg, want := range cases {
		if got := providerIdentifier(pkg); got != want {
			t.Errorf("providerIdentifier(%q) = %q, want %q", pkg, got, want)
		}
	}
}
