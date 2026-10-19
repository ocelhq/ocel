package project

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
)

func TestAProjectNamingNoAppsHasTheAppAtItsRootInEveryLanguageShapedAsIfNamedThere(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		files     map[string]string
		framework string
	}{
		{"node", map[string]string{nodeManifest: `{"dependencies":{"express":"5"}}`, "src/server.ts": "export {}\n"}, buildoutput.FrameworkNode},
		{"next", map[string]string{nodeManifest: `{"dependencies":{"next":"15"}}`}, buildoutput.FrameworkNext},
		{"sveltekit", map[string]string{nodeManifest: `{"devDependencies":{"@sveltejs/kit":"2"}}`}, buildoutput.FrameworkSvelteKit},
		{"python", map[string]string{"pyproject.toml": "[project]\nname = \"api\"\n", "main.py": "\n"}, buildoutput.FrameworkPython},
		{"go", map[string]string{"go.mod": "module example.com/api\n", "main.go": "package main\n\nfunc main() {}\n"}, buildoutput.FrameworkGo},
		{"rust", map[string]string{"Cargo.toml": "[package]\nname = \"api\"\n", "src/main.rs": "fn main() {}\n"}, buildoutput.FrameworkRust},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			load := func(config string) *Project {
				dir := t.TempDir()
				write(t, filepath.Join(dir, DefaultFileName), config)
				for name, body := range tc.files {
					write(t, filepath.Join(dir, filepath.FromSlash(name)), body)
				}
				cfg, err := Load(context.Background(), dir, "")
				if err != nil {
					t.Fatalf("Load: %v", err)
				}
				for i, app := range cfg.Apps {
					if app.GoModule == nil {
						continue
					}
					if app.GoModule.Dir != dir {
						t.Errorf("Apps[%d].GoModule = %+v, want the module at %s", i, app.GoModule, dir)
					}
					module := *app.GoModule
					module.Dir = ""
					cfg.Apps[i].GoModule = &module
				}
				return cfg
			}
			detected := load(`{"slug":"shop"}`).Apps
			named := load(`{"slug":"shop","apps":[{"name":"shop","path":"."}]}`).Apps
			if len(detected) != 1 || detected[0].Name != "shop" || detected[0].Path != "." || detected[0].Framework != tc.framework {
				t.Fatalf("Apps = %+v, want one %s app named %q at %q", detected, tc.framework, "shop", ".")
			}
			if !reflect.DeepEqual(detected, named) {
				t.Errorf("detected root app = %+v, want it shaped as the same root named under \"apps\": %+v", detected[0], named[0])
			}
		})
	}
}

func TestAProjectNamingNoAppsHasNoAppAtARootNoBuildWouldServe(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		files map[string]string
		why   string
	}{
		{"a package.json with no framework and no entrypoint", map[string]string{nodeManifest: `{"dependencies":{"ocel":"0.0.2"}}`}, "a package.json with no next dependency or next config, no @sveltejs/kit dependency and no entrypoint among src/server"},
		{"a go module whose main package is under cmd/worker", map[string]string{"go.mod": "module example.com/api\n", "cmd/worker/main.go": "package main\n\nfunc main() {}\n"}, "a go.mod with no package main in ., cmd/api or cmd/server"},
		{"a library crate", map[string]string{"Cargo.toml": "[package]\nname = \"api\"\n", "src/lib.rs": "\n"}, "a Cargo.toml with no binary target"},
		{"a pyproject.toml with no entrypoint", map[string]string{"pyproject.toml": "[project]\nname = \"helpers\"\n"}, "a pyproject.toml with no app.py"},
		{"a package.json that is not JSON", map[string]string{nodeManifest: "{"}, "a package.json that cannot be read"},
		{"a Cargo.toml that is not TOML", map[string]string{"Cargo.toml": "[package\n", "src/main.rs": "fn main() {}\n"}, "a Cargo.toml that cannot be read"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			write(t, filepath.Join(dir, DefaultFileName), `{"slug":"shop"}`)
			for name, body := range tc.files {
				write(t, filepath.Join(dir, filepath.FromSlash(name)), body)
			}
			cfg, err := Load(context.Background(), dir, "")
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if len(cfg.Apps) != 0 {
				t.Errorf("Apps = %+v, want none", cfg.Apps)
			}
			if cfg.RootAppUndetected == nil || !strings.HasPrefix(cfg.RootAppUndetected.Error(), "the project root holds "+tc.why) {
				t.Errorf("RootAppUndetected = %v, want it to say the root holds %s", cfg.RootAppUndetected, tc.why)
			}
		})
	}
}

func TestANamedServerlessAppWhoseDirectoryHoldsTwoAppsIsRefusedAtLoadNamingFramework(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{"slug":"shop","apps":[{"name":"api","path":".","entrypoint":"cmd/api","compute":"serverless"}]}`)
	write(t, filepath.Join(dir, "go.mod"), "module example.com/api\n")
	write(t, filepath.Join(dir, "cmd", "api", "main.go"), "package main\n\nfunc main() {}\n")
	write(t, filepath.Join(dir, nodeManifest), `{"dependencies":{"next":"15"}}`)

	_, err := Load(context.Background(), dir, "")
	want := `app "api": the project root holds both a Next.js app (package.json depends on next) and a go app (package main in cmd/api): set "framework"`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Load err = %v, want it to contain %q", err, want)
	}
}

func TestANamedAppSettingItsFrameworkTakesThatFrameworksEntrypoint(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{"slug":"shop","apps":[{"name":"api","path":".","framework":"go","compute":"serverless"}]}`)
	write(t, filepath.Join(dir, "go.mod"), "module example.com/api\n")
	write(t, filepath.Join(dir, "cmd", "api", "main.go"), "package main\n\nfunc main() {}\n")
	write(t, filepath.Join(dir, nodeManifest), `{"dependencies":{"next":"15"}}`)

	cfg, err := Load(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if api := cfg.Apps[0]; api.Framework != buildoutput.FrameworkGo || api.Language != language.Go || api.Entrypoint != "cmd/api" || api.FrameworkSource != language.Configured || api.GoPackage != "./cmd/api" {
		t.Errorf("api = %+v, want go from the config with entrypoint cmd/api", api)
	}
}

func TestANamedAppsEntrypointReplacesTheDetectedOne(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{"slug":"shop","apps":[{"name":"api","path":".","entrypoint":"src/main.ts"}]}`)
	write(t, filepath.Join(dir, nodeManifest), `{}`)
	write(t, filepath.Join(dir, "src", "index.ts"), "\n")

	cfg, err := Load(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if api := cfg.Apps[0]; api.Framework != buildoutput.FrameworkNode || api.Entrypoint != "src/main.ts" || api.Evidence != "src/index.ts" {
		t.Errorf("api = %+v, want node detected from src/index.ts, starting at src/main.ts", api)
	}
}

func TestAnEntrypointOnANextAppIsRefusedAtLoad(t *testing.T) {
	t.Parallel()

	_, err := loadJSON(t, `{"slug":"shop","apps":[{"name":"web","path":"web","framework":"next","entrypoint":"server.js"}]}`)
	var invalid *clierror.Error
	if !errors.As(err, &invalid) || invalid.Hint != "apps[0].entrypoint" || !strings.Contains(err.Error(), `drop "entrypoint"`) {
		t.Fatalf("Load err = %v, want the entrypoint refused at apps[0].entrypoint", err)
	}
}

func TestAnEntrypointInTheWrongFormForItsFrameworkIsRefusedAtLoad(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		framework, entrypoint, want string
	}{
		{"go", "/srv/cmd/api", "directory"},
		{"go", "../cmd/api", "directory"},
	} {
		t.Run(tt.framework+" "+tt.entrypoint, func(t *testing.T) {
			t.Parallel()
			_, err := loadJSON(t, `{"slug":"shop","apps":[{"name":"api","path":"api","framework":"`+tt.framework+`","entrypoint":"`+tt.entrypoint+`","compute":"serverless"}]}`)
			var invalid *clierror.Error
			if !errors.As(err, &invalid) || invalid.Hint != "apps[0].entrypoint" || !strings.Contains(err.Error(), tt.entrypoint) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load err = %v, want %q refused at apps[0].entrypoint as no %s", err, tt.entrypoint, tt.want)
			}
		})
	}
}

func TestAnEntrypointInTheFormOfItsFrameworkIsTakenAtLoad(t *testing.T) {
	t.Parallel()

	for _, tt := range [][2]string{{"go", "cmd/api"}, {"go", "."}} {
		framework, entrypoint := tt[0], tt[1]
		cfg, err := loadJSON(t, `{"slug":"shop","apps":[{"name":"api","path":"api","framework":"`+framework+`","entrypoint":"`+entrypoint+`","compute":"serverless"}]}`)
		if err != nil {
			t.Fatalf("Load %s %s: %v", framework, entrypoint, err)
		}
		if got := cfg.Apps[0].Entrypoint; got != entrypoint {
			t.Errorf("%s entrypoint = %q, want %q", framework, got, entrypoint)
		}
	}
}

func TestAGoAppInsideTheProjectsModuleIsDetected(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{"slug":"shop","apps":[{"name":"api","path":"services/api","compute":"serverless"}]}`)
	write(t, filepath.Join(dir, "go.mod"), "module example.com/shop\n")
	write(t, filepath.Join(dir, "services", "api", "main.go"), "package main\n\nfunc main() {}\n")

	cfg, err := Load(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if api := cfg.Apps[0]; api.Framework != buildoutput.FrameworkGo || api.Entrypoint != "." || api.Evidence != "package main" {
		t.Errorf("api = %+v, want go detected in the project's module", api)
	}
}

func TestTheRootAppOfANextProjectIsBuiltWithNext(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{"slug":"shop"}`)
	write(t, filepath.Join(dir, nodeManifest), `{"dependencies":{"next":"15"}}`)

	cfg, err := Load(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Apps) != 1 || cfg.Apps[0].Framework != buildoutput.FrameworkNext {
		t.Errorf("Apps = %+v, want one next app", cfg.Apps)
	}
}

func TestAProjectNamingNoAppsHasTheNextAppAtItsRootBesideAPyprojectWithNoMainPy(t *testing.T) {
	t.Parallel()

	for _, config := range []string{`{"slug":"shop"}`, `{"slug":"shop","apps":[]}`} {
		t.Run(config, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			write(t, filepath.Join(dir, DefaultFileName), config)
			write(t, filepath.Join(dir, nodeManifest), `{"dependencies":{"next":"15","react":"19"}}`)
			write(t, filepath.Join(dir, "next.config.mjs"), "export default {}\n")
			write(t, filepath.Join(dir, "app", "page.tsx"), "export default function Page() { return null }\n")
			write(t, filepath.Join(dir, "pyproject.toml"), "[project]\nname = \"helpers\"\n")

			cfg, err := Load(context.Background(), dir, "")
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if len(cfg.Apps) != 1 || cfg.Apps[0].Name != "shop" || cfg.Apps[0].Framework != buildoutput.FrameworkNext {
				t.Errorf("Apps = %+v, want the next app at the project root: the pyproject.toml holds no app python serves", cfg.Apps)
			}
		})
	}
}

func TestAProjectNamingNoAppsSaysWhyNoAppIsDetectedAtARootHoldingTwoApps(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{"slug":"shop"}`)
	write(t, filepath.Join(dir, nodeManifest), `{"dependencies":{"next":"15"}}`)
	write(t, filepath.Join(dir, "pyproject.toml"), "[project]\nname = \"api\"\n")
	write(t, filepath.Join(dir, "main.py"), "app = None\n")

	cfg, err := Load(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Apps) != 0 {
		t.Errorf("Apps = %+v, want none: nothing says which of the two is the app", cfg.Apps)
	}
	if cfg.RootAppUndetected == nil {
		t.Fatal("RootAppUndetected = nil, want why no app is detected at the project root")
	}
	if want := "the project root holds both a Next.js app (package.json depends on next) and a python app (main.py)"; cfg.RootAppUndetected.Error() != want {
		t.Errorf("RootAppUndetected = %q, want %q", cfg.RootAppUndetected, want)
	}
}

func TestAProjectNamingNoAppsWithNoManifestAtItsRootSaysWhatWasLookedFor(t *testing.T) {
	t.Parallel()

	cfg := mustLoadJSON(t, `{"slug":"shop"}`)
	want := "the project root holds none of package.json, pyproject.toml, requirements.txt, Pipfile, go.mod or Cargo.toml"
	if cfg.RootAppUndetected == nil || cfg.RootAppUndetected.Error() != want {
		t.Errorf("RootAppUndetected = %v, want %q", cfg.RootAppUndetected, want)
	}
}

func TestAProjectWithNoConfigSaysWhyNoAppIsDetectedAtARootHoldingTwoApps(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write(t, filepath.Join(dir, nodeManifest), `{"dependencies":{"next":"15"}}`)
	write(t, filepath.Join(dir, "requirements.txt"), "fastapi\n")
	write(t, filepath.Join(dir, "main.py"), "app = None\n")

	cfg, err := LoadOptional(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("LoadOptional: %v", err)
	}
	if len(cfg.Apps) != 0 || cfg.RootAppUndetected == nil {
		t.Errorf("Apps = %+v, RootAppUndetected = %v, want no app and why", cfg.Apps, cfg.RootAppUndetected)
	}
}

func TestARootWithAGoModuleBesideAPackageJSONIsReadAsGoWhetherOrNotItNamesApps(t *testing.T) {
	t.Parallel()

	t.Run("naming no apps, its go main package is the app at its root", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		write(t, filepath.Join(dir, DefaultFileName), `{"slug":"shop"}`)
		write(t, filepath.Join(dir, "go.mod"), "module example.com/shop\n")
		write(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}\n")
		write(t, filepath.Join(dir, nodeManifest), `{"devDependencies":{"tailwindcss":"4"}}`)

		cfg, err := Load(context.Background(), dir, "")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(cfg.Apps) != 1 || cfg.Apps[0].Framework != buildoutput.FrameworkGo {
			t.Errorf("Apps = %+v, want one go app: the package.json beside a go main package lists the go project's tooling", cfg.Apps)
		}
	})

	t.Run("naming its root as an app, that app is built with go", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		write(t, filepath.Join(dir, DefaultFileName), `{"slug":"shop","apps":[{"name":"shop","path":"."}]}`)
		write(t, filepath.Join(dir, "go.mod"), "module example.com/shop\n")
		write(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}\n")
		write(t, filepath.Join(dir, nodeManifest), `{"devDependencies":{"tailwindcss":"4"}}`)

		cfg, err := Load(context.Background(), dir, "")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(cfg.Apps) != 1 || cfg.Apps[0].Framework != buildoutput.FrameworkGo {
			t.Errorf("Apps = %+v, want one go app: the same root decides the same language however it is named", cfg.Apps)
		}
	})
}

func TestAProjectWithNoPackageJSONAtItsRootAndNoAppsDeploysNoApp(t *testing.T) {
	t.Parallel()

	cfg := mustLoadJSON(t, `{"slug":"shop"}`)
	if len(cfg.Apps) != 0 {
		t.Errorf("Apps = %+v, want none: nothing at the root says an app is there", cfg.Apps)
	}
}

func TestAProjectWithNoConfigHasTheRootAppNamedAsInitWouldNameTheProject(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "My Shop")
	write(t, filepath.Join(dir, nodeManifest), `{}`)
	write(t, filepath.Join(dir, "index.js"), "\n")

	cfg, err := LoadOptional(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("LoadOptional: %v", err)
	}
	if len(cfg.Apps) != 1 || cfg.Apps[0].Name != "my-shop" {
		t.Errorf("Apps = %+v, want one app named %q", cfg.Apps, "my-shop")
	}
}

func TestAnAppTakingTheInfrastructureStacksNameIsRefusedAtLoad(t *testing.T) {
	t.Parallel()

	_, err := loadJSON(t, `{"slug":"shop","apps":[{"name":"`+naming.InfraApp+`","path":"."}]}`)
	if err == nil || !strings.Contains(err.Error(), strconv.Quote(naming.InfraApp)+" is reserved") {
		t.Fatalf("Load err = %v, want the reserved name refused", err)
	}
}

func TestASlugTakingTheInfrastructureStacksNameIsRefusedWhenItWouldNameTheRootApp(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{"slug":"`+naming.InfraApp+`"}`)
	write(t, filepath.Join(dir, nodeManifest), `{}`)
	write(t, filepath.Join(dir, "index.js"), "\n")

	_, err := Load(context.Background(), dir, "")
	if err == nil || !strings.Contains(err.Error(), strconv.Quote(naming.InfraApp)+" is reserved") {
		t.Fatalf("Load err = %v, want the root app's reserved name refused", err)
	}
}

func TestAFrameworkOrEntrypointUnderAComputeIsRefusedAtLoadAsTheApps(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ compute, name, config string }{
		{"container", "framework", `"framework":"node"`},
		{"container", "entrypoint", `"entrypoint":"server.js"`},
		{"serverless", "framework", `"framework":"node"`},
		{"serverless", "entrypoint", `"entrypoint":"server.js"`},
	} {
		t.Run(tc.compute+" "+tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := loadJSON(t, `{"slug":"shop","apps":[{"name":"api","path":"api","compute":{"`+tc.compute+`":{`+tc.config+`}}}]}`)
			if err == nil || !strings.Contains(err.Error(), `"apps[0].compute.`+tc.compute+`.`+tc.name+`" is not a key`) || !strings.Contains(err.Error(), tc.name+" is an option of the app") {
				t.Fatalf("Load err = %v, want the %s refused under compute and named as the app's", err, tc.name)
			}
		})
	}
}

func TestAnAppDeclaringServerlessComputeAndContainerConfigIsRefusedAtLoad(t *testing.T) {
	t.Parallel()

	for name, config := range map[string]string{
		"image":     `"image":{"dockerfile":"Dockerfile"}`,
		"health":    `"health":{"path":"/healthz"}`,
		"instances": `"instances":{"max":3}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := loadJSON(t, `{"slug":"shop","apps":[{"name":"api","path":"api","framework":"node","compute":{"serverless":{`+config+`}}}]}`)
			if err == nil || !strings.Contains(err.Error(), `"apps[0].compute.serverless.`+name+`" is not a key`) {
				t.Fatalf("Load err = %v, want the %s refused on an app that runs as functions", err, name)
			}
		})
	}
}

func TestAnAppDeclaringAComputeHasOnlyThatComputesShape(t *testing.T) {
	t.Parallel()

	cfg := mustLoadJSON(t, `{"slug":"shop","apps":[
		{"name":"api","path":"api","compute":{"container":{"image":{"command":"make"}}}},
		{"name":"web","path":"web","framework":"node","entrypoint":"server.js","compute":"serverless"}
	]}`)
	if api := cfg.Apps[0]; api.Compute != provider.ComputeContainer || api.Container == nil || api.Container.Build.Command != "make" {
		t.Errorf("api = %+v, want the container shape alone, carrying its image", api)
	}
	if web := cfg.Apps[1]; web.Compute != provider.ComputeServerless || web.Container != nil || web.Framework != "node" || web.Entrypoint != "server.js" {
		t.Errorf("web = %+v, want the serverless shape alone, carrying its entrypoint", web)
	}
}

func TestAnAppNamingItsComputeAloneHasThatComputesShape(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{"slug":"shop","apps":[
		{"name":"api","path":"api","compute":"container"},
		{"name":"web","path":"web","compute":"serverless"}
	]}`)
	write(t, filepath.Join(dir, "web", nodeManifest), `{"dependencies":{"express":"5"}}`)
	write(t, filepath.Join(dir, "web", "src", "server.ts"), "\n")

	cfg, err := Load(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if api := cfg.Apps[0]; api.Compute != provider.ComputeContainer || api.Container == nil {
		t.Errorf("api = %+v, want the container shape alone", api)
	}
	if web := cfg.Apps[1]; web.Compute != provider.ComputeServerless || web.Container != nil || web.Framework != buildoutput.FrameworkNode {
		t.Errorf("web = %+v, want the serverless shape alone, built with the framework read off its manifest", web)
	}
}

func TestAContainerAppInANextDirectoryIsBuiltWithNextWithoutNamingIt(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{"slug":"shop","apps":[{"name":"web","path":"web","compute":"container"}]}`)
	write(t, filepath.Join(dir, "web", nodeManifest), `{}`)
	write(t, filepath.Join(dir, "web", "next.config.js"), "module.exports = {};\n")

	cfg, err := Load(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if web := cfg.Apps[0]; web.Container == nil || web.Framework != buildoutput.FrameworkNext || web.Evidence != "next.config.js" {
		t.Errorf("web = %+v, want a container built with next", web)
	}
}

func TestAContainerAppKeepsTheInstanceCountsItNames(t *testing.T) {
	t.Parallel()

	cfg := mustLoadJSON(t, `{"slug":"shop","apps":[{"name":"api","path":"api","compute":{"container":{"instances":{"min":0,"max":4}}}}]}`)
	if got, want := cfg.Apps[0].Container.Instances(), (provider.Instances{Min: 0, Max: 4}); got != want {
		t.Errorf("Instances() = %+v, want %+v", got, want)
	}
}

func TestAContainerAppLeftWithoutInstanceCountsRunsOneOrAsManyAsItsFloor(t *testing.T) {
	t.Parallel()

	for config, want := range map[string]provider.Instances{
		`{}`:                      {Min: 1, Max: 1},
		`{"instances":{}}`:        {Min: 1, Max: 1},
		`{"instances":{"min":3}}`: {Min: 3, Max: 3},
		`{"instances":{"min":0}}`: {Min: 0, Max: 1},
		`{"instances":{"max":5}}`: {Min: 1, Max: 5},
	} {
		cfg := mustLoadJSON(t, `{"slug":"shop","apps":[{"name":"api","path":"api","compute":{"container":`+config+`}}]}`)
		if got := cfg.Apps[0].Container.Instances(); got != want {
			t.Errorf("%s: Instances() = %+v, want %+v", config, got, want)
		}
	}
}

func TestAContainerAppWhoseCeilingIsBelowItsFloorIsRefusedAtLoad(t *testing.T) {
	t.Parallel()

	_, err := loadJSON(t, `{"slug":"shop","apps":[{"name":"api","path":"api","compute":{"container":{"instances":{"min":3,"max":2}}}}]}`)
	if err == nil {
		t.Fatal("Load admitted an app that runs at least 3 instances and at most 2")
	}
	for _, want := range []string{`app "api"`, "instances.min 3", "instances.max 2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Load err = %q, want it to name %s", err, want)
		}
	}
}

func TestAnInstanceCountNoProviderCanHoldIsRefusedAtLoad(t *testing.T) {
	t.Parallel()

	_, err := loadJSON(t, `{"slug":"shop","apps":[{"name":"api","path":"api","compute":{"container":{"instances":{"max":4294967297}}}}]}`)
	if err == nil {
		t.Fatal("Load admitted 4294967297 instances, which wraps to 1 on the wire")
	}
	for _, want := range []string{`app "api"`, "instances.max", "2147483647"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Load err = %q, want it to name %s", err, want)
		}
	}
}
