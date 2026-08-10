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
)

func stubPackageManager(t *testing.T, result error) *[]string {
	t.Helper()
	var argv []string
	prev := runPackageManager
	runPackageManager = func(_ context.Context, _ string, cmd []string, _ io.Writer) error {
		argv = cmd
		return result
	}
	t.Cleanup(func() { runPackageManager = prev })
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

func TestRunInit_NoArgument_DefaultsSlugToDirectoryName(t *testing.T) {
	stubPackageManager(t, nil)
	dir := initTestDir(t, "My Cool App")

	var stdout bytes.Buffer
	if err := runInit(context.Background(), dir, "", initOptions{}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("runInit err = %v; stdout=%s", err, stdout.String())
	}

	content := readConfig(t, dir)
	if !strings.Contains(content, `slug: "my-cool-app"`) {
		t.Fatalf("config = %q, want slug derived from the directory name", content)
	}
}

func TestRunInit_ExplicitSlug_WritesDeployableConfig(t *testing.T) {
	stubPackageManager(t, nil)
	dir := initTestDir(t, "ignored-dir-name")

	var stdout bytes.Buffer
	if err := runInit(context.Background(), dir, "my-app", initOptions{}, &stdout, &bytes.Buffer{}); err != nil {
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
}

func TestRunInit_InvalidSlug_ErrorsWithoutWritingConfig(t *testing.T) {
	for _, slug := range []string{"My App", "-leading", "trailing-", "under_score", strings.Repeat("a", 64)} {
		t.Run(slug, func(t *testing.T) {
			stubPackageManager(t, nil)
			dir := initTestDir(t, "proj")

			err := runInit(context.Background(), dir, slug, initOptions{}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil {
				t.Fatal("runInit err = nil, want error")
			}
			if !strings.Contains(err.Error(), "invalid slug") {
				t.Fatalf("err = %v, want it to name the slug as invalid", err)
			}
			if _, statErr := os.Stat(filepath.Join(dir, "ocel.config.ts")); !os.IsNotExist(statErr) {
				t.Fatal("ocel.config.ts should not have been written")
			}
		})
	}
}

func TestRunInit_UnslugifiableDirectoryName_ErrorsAskingForASlug(t *testing.T) {
	stubPackageManager(t, nil)
	dir := initTestDir(t, "!!!")

	err := runInit(context.Background(), dir, "", initOptions{}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("runInit err = nil, want error")
	}
	if !strings.Contains(err.Error(), "ocel init my-app") {
		t.Fatalf("err = %v, want it to show how to pass a slug", err)
	}
}

func TestRunInit_ExistingConfig_RefusesToOverwrite(t *testing.T) {
	argv := stubPackageManager(t, nil)
	dir := initTestDir(t, "proj")
	configPath := filepath.Join(dir, "ocel.config.ts")
	if err := os.WriteFile(configPath, []byte("existing"), 0o644); err != nil {
		t.Fatalf("write existing config: %v", err)
	}

	err := runInit(context.Background(), dir, "my-app", initOptions{}, &bytes.Buffer{}, &bytes.Buffer{})
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
}

func TestRunInit_ProviderFlag_OverridesTheDefaultPackage(t *testing.T) {
	argv := stubPackageManager(t, nil)
	dir := initTestDir(t, "proj")

	opts := initOptions{provider: "@acme/provider-gcp"}
	if err := runInit(context.Background(), dir, "my-app", opts, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runInit err = %v", err)
	}

	content := readConfig(t, dir)
	if !strings.Contains(content, `import gcpProvider from "@acme/provider-gcp";`) || !strings.Contains(content, "provider: gcpProvider()") {
		t.Fatalf("config = %q, want it to use the overridden provider", content)
	}
	if got := *argv; !slices.Equal(got, []string{"npm", "install", sdkPackage, "@acme/provider-gcp"}) {
		t.Fatalf("ran %v, want the overridden package added alongside %s", got, sdkPackage)
	}
}

func TestRunInit_InstallsTheSDKAlongsideTheProvider(t *testing.T) {
	argv := stubPackageManager(t, nil)
	dir := initTestDir(t, "proj")

	var stdout bytes.Buffer
	if err := runInit(context.Background(), dir, "my-app", initOptions{}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("runInit err = %v", err)
	}

	want := []string{"npm", "install", sdkPackage, defaultProviderPackage}
	if got := *argv; !slices.Equal(got, want) {
		t.Fatalf("ran %v, want %v", got, want)
	}
	if !strings.Contains(readConfig(t, dir), `from "`+sdkPackage+`/config"`) {
		t.Fatalf("config = %q, want it to import from %s/config", readConfig(t, dir), sdkPackage)
	}
}

func TestRunInit_AddsProviderWithThePackageManagerTheLockfileNames(t *testing.T) {
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
			name = "no-lockfile"
		}
		t.Run(name, func(t *testing.T) {
			argv := stubPackageManager(t, nil)
			dir := initTestDir(t, "proj")
			if tc.lockfile != "" {
				if err := os.WriteFile(filepath.Join(dir, tc.lockfile), nil, 0o644); err != nil {
					t.Fatalf("write lockfile: %v", err)
				}
			}

			var stdout bytes.Buffer
			if err := runInit(context.Background(), dir, "my-app", initOptions{}, &stdout, &bytes.Buffer{}); err != nil {
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
}

func TestRunInit_NoPackageJSON_SkipsTheInstallAndSaysSo(t *testing.T) {
	argv := stubPackageManager(t, nil)
	dir := initTestDir(t, "proj")
	if err := os.Remove(filepath.Join(dir, "package.json")); err != nil {
		t.Fatalf("remove package.json: %v", err)
	}

	var stdout bytes.Buffer
	if err := runInit(context.Background(), dir, "my-app", initOptions{}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("runInit err = %v", err)
	}
	if *argv != nil {
		t.Fatalf("ran %v, want no package manager call", *argv)
	}
	if !strings.Contains(stdout.String(), "npm install "+sdkPackage+" "+defaultProviderPackage) {
		t.Fatalf("stdout = %q, want the command to run later", stdout.String())
	}
}

func TestRunInit_PackageManagerFails_KeepsTheConfigAndPrintsTheCommand(t *testing.T) {
	stubPackageManager(t, errors.New("exec: \"pnpm\": executable file not found in $PATH"))
	dir := initTestDir(t, "proj")
	if err := os.WriteFile(filepath.Join(dir, "pnpm-lock.yaml"), nil, 0o644); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}

	var stdout bytes.Buffer
	if err := runInit(context.Background(), dir, "my-app", initOptions{}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("runInit err = %v, want the failed install to be non-fatal", err)
	}
	if !strings.Contains(readConfig(t, dir), `slug: "my-app"`) {
		t.Fatal("config should still have been written")
	}
	if !strings.Contains(stdout.String(), "pnpm add "+sdkPackage+" "+defaultProviderPackage) {
		t.Fatalf("stdout = %q, want the command the user should run", stdout.String())
	}
}

func TestProviderIdentifier(t *testing.T) {
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
