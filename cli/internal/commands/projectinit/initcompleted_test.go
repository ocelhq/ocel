package projectinit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/telemetry"
)

func recordInits(dependencies *Dependencies) *[]telemetry.Payload {
	var recorded []telemetry.Payload
	dependencies.RecordEvent = func(payload telemetry.Payload) { recorded = append(recorded, payload) }
	return &recorded
}

func TestASuccessfulInitRecordsTheLanguagePackageManagerProviderAndConfigFormat(t *testing.T) {
	cases := map[string]struct {
		files []string
		opts  initOptions
		want  telemetry.InitCompletion
	}{
		"a node project with a pnpm lockfile and the default config": {
			files: []string{"package.json", "pnpm-lock.yaml"},
			opts:  initOptions{provider: "fake"},
			want:  telemetry.InitCompletion{Language: "node", PackageManager: "pnpm", Provider: "fake", ConfigFormat: "json"},
		},
		"a node project with no lockfile falls back to npm and writes yaml": {
			files: []string{"package.json"},
			opts:  initOptions{provider: "fake", yaml: true},
			want:  telemetry.InitCompletion{Language: "node", PackageManager: "npm", Provider: "fake", ConfigFormat: "yaml"},
		},
		"a go module writes a typescript config": {
			files: []string{"go.mod"},
			opts:  initOptions{provider: "fake", ts: true},
			want:  telemetry.InitCompletion{Language: "go", PackageManager: "go", Provider: "fake", ConfigFormat: "ts"},
		},
		"a language named by flag wins over the manifests": {
			files: []string{"package.json"},
			opts:  initOptions{provider: "fake", language: "python"},
			want:  telemetry.InitCompletion{Language: "python", PackageManager: "uv", Provider: "fake", ConfigFormat: "json"},
		},
		"a directory with no manifest sends no language or package manager": {
			opts: initOptions{provider: "fake"},
			want: telemetry.InitCompletion{Provider: "fake", ConfigFormat: "json"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dependencies := newTestDependencies()
			stubPackageManager(&dependencies, nil)
			recorded := recordInits(&dependencies)
			dir := filepath.Join(t.TempDir(), "proj")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, file := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, file), []byte("{}\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			if _, err := runInit(context.Background(), dependencies, dir, "my-app", tc.opts); err != nil {
				t.Fatalf("runInit err = %v", err)
			}

			if len(*recorded) != 1 || (*recorded)[0] != tc.want {
				t.Errorf("recorded = %+v, want exactly [%+v]", *recorded, tc.want)
			}
		})
	}
}

func TestAnInitThatFailsRecordsNothing(t *testing.T) {
	dependencies := newTestDependencies()
	stubPackageManager(&dependencies, nil)
	recorded := recordInits(&dependencies)
	dir := initTestDir(t, "proj")
	if err := os.WriteFile(filepath.Join(dir, "ocel.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := runInit(context.Background(), dependencies, dir, "", initOptions{provider: "fake"})

	if err == nil || len(*recorded) != 0 {
		t.Errorf("err = %v, recorded = %+v, want a refusal and no event", err, *recorded)
	}
}

func TestAnInitWhoseSDKInstallFailsStillRecordsAsCompleted(t *testing.T) {
	dependencies := newTestDependencies()
	stubPackageManager(&dependencies, os.ErrPermission)
	recorded := recordInits(&dependencies)
	dir := initTestDir(t, "proj")

	_, err := runInit(context.Background(), dependencies, dir, "", initOptions{provider: "fake"})

	if err != nil || len(*recorded) != 1 {
		t.Errorf("err = %v, recorded = %+v, want one event: the config was written and init succeeded", err, *recorded)
	}
}

func TestNoNameOrPathReachesTheInitCompletion(t *testing.T) {
	dependencies := newTestDependencies()
	stubPackageManager(&dependencies, nil)
	recorded := recordInits(&dependencies)
	dir := initTestDir(t, "my-secret-project")

	if _, err := runInit(context.Background(), dependencies, dir, "my-secret-slug", initOptions{provider: "fake"}); err != nil {
		t.Fatal(err)
	}

	completion := (*recorded)[0].(telemetry.InitCompletion)
	for _, value := range []string{completion.Language, completion.PackageManager, completion.Provider, completion.ConfigFormat} {
		if strings.Contains(value, "secret") || strings.Contains(value, string(filepath.Separator)) {
			t.Errorf("completion value %q names the project or a path", value)
		}
	}
}
