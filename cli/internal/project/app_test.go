package project

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
)

func TestAProjectNamingNoAppsHasTheNodeAppAtItsRootNamedAfterItsSlug(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{"slug":"shop"}`)
	write(t, filepath.Join(dir, nodeManifest), `{"dependencies":{"express":"5"}}`)

	cfg, err := Load(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Apps) != 1 {
		t.Fatalf("Apps = %+v, want the one app at the project root", cfg.Apps)
	}
	if app := cfg.Apps[0]; app.Name != "shop" || app.Path != "." || app.Framework() != buildoutput.FrameworkNode {
		t.Errorf("the root app = %+v, want a node app named %q at %q", app, "shop", ".")
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
	if len(cfg.Apps) != 1 || cfg.Apps[0].Framework() != buildoutput.FrameworkNext {
		t.Errorf("Apps = %+v, want one next app", cfg.Apps)
	}
}

func TestARootWithAGoModuleBesideAPackageJSONIsReadAsGoWhetherOrNotItNamesApps(t *testing.T) {
	t.Parallel()

	t.Run("naming no apps, it has no node app at its root", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		write(t, filepath.Join(dir, DefaultFileName), `{"slug":"shop"}`)
		write(t, filepath.Join(dir, "go.mod"), "module example.com/shop\n")
		write(t, filepath.Join(dir, nodeManifest), `{"devDependencies":{"tailwindcss":"4"}}`)

		cfg, err := Load(context.Background(), dir, "")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(cfg.Apps) != 0 {
			t.Errorf("Apps = %+v, want none: the package.json beside a go module lists the go project's tooling", cfg.Apps)
		}
	})

	t.Run("naming its root as an app, that app is built with go", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		write(t, filepath.Join(dir, DefaultFileName), `{"slug":"shop","apps":[{"name":"shop","path":"."}]}`)
		write(t, filepath.Join(dir, "go.mod"), "module example.com/shop\n")
		write(t, filepath.Join(dir, nodeManifest), `{"devDependencies":{"tailwindcss":"4"}}`)

		cfg, err := Load(context.Background(), dir, "")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(cfg.Apps) != 1 || cfg.Apps[0].Framework() != buildoutput.FrameworkGo {
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

	_, err := Load(context.Background(), dir, "")
	if err == nil || !strings.Contains(err.Error(), strconv.Quote(naming.InfraApp)+" is reserved") {
		t.Fatalf("Load err = %v, want the root app's reserved name refused", err)
	}
}

func TestAnAppDeclaringContainerComputeAndAFrameworkIsRefusedAtLoad(t *testing.T) {
	t.Parallel()

	_, err := loadJSON(t, `{"slug":"shop","apps":[{"name":"api","path":"api","compute":"container","framework":"node"}]}`)
	if err == nil || !strings.Contains(err.Error(), "`framework`") {
		t.Fatalf("Load err = %v, want the framework refused on an app that runs the image it is given", err)
	}
}

func TestAnAppDeclaringServerlessComputeAndContainerConfigIsRefusedAtLoad(t *testing.T) {
	t.Parallel()

	for name, config := range map[string]string{
		"build":        `"build":{"dockerfile":"Dockerfile"}`,
		"health":       `"health":{"path":"/healthz"}`,
		"minInstances": `"minInstances":1`,
		"maxInstances": `"maxInstances":3`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := loadJSON(t, `{"slug":"shop","apps":[{"name":"api","path":"api","compute":"serverless","framework":"node",`+config+`}]}`)
			if err == nil || !strings.Contains(err.Error(), "`"+name+"`") {
				t.Fatalf("Load err = %v, want the %s refused on an app that runs as functions", err, name)
			}
		})
	}
}

func TestAnAppDeclaringAComputeHasOnlyThatComputesShape(t *testing.T) {
	t.Parallel()

	cfg := mustLoadJSON(t, `{"slug":"shop","apps":[
		{"name":"api","path":"api","compute":"container","build":{"command":"make"}},
		{"name":"web","path":"web","compute":"serverless","framework":"node"}
	]}`)
	if api := cfg.Apps[0]; api.Serverless != nil || api.Container == nil || api.Container.Build.Command != "make" {
		t.Errorf("api = %+v, want the container shape alone, carrying its build", api)
	}
	if web := cfg.Apps[1]; web.Container != nil || web.Framework() != "node" {
		t.Errorf("web = %+v, want the serverless shape alone", web)
	}
}

func TestAContainerAppKeepsTheInstanceCountsItNames(t *testing.T) {
	t.Parallel()

	cfg := mustLoadJSON(t, `{"slug":"shop","apps":[{"name":"api","path":"api","compute":"container","minInstances":0,"maxInstances":4}]}`)
	if got, want := cfg.Apps[0].Container.Instances(), (provider.Instances{Min: 0, Max: 4}); got != want {
		t.Errorf("Instances() = %+v, want %+v", got, want)
	}
}

func TestAContainerAppLeftWithoutInstanceCountsRunsOneOrAsManyAsItsFloor(t *testing.T) {
	t.Parallel()

	for config, want := range map[string]provider.Instances{
		``:                  {Min: 1, Max: 1},
		`,"minInstances":3`: {Min: 3, Max: 3},
		`,"minInstances":0`: {Min: 0, Max: 1},
		`,"maxInstances":5`: {Min: 1, Max: 5},
	} {
		cfg := mustLoadJSON(t, `{"slug":"shop","apps":[{"name":"api","path":"api","compute":"container"`+config+`}]}`)
		if got := cfg.Apps[0].Container.Instances(); got != want {
			t.Errorf("%s: Instances() = %+v, want %+v", config, got, want)
		}
	}
}

func TestAContainerAppWhoseCeilingIsBelowItsFloorIsRefusedAtLoad(t *testing.T) {
	t.Parallel()

	_, err := loadJSON(t, `{"slug":"shop","apps":[{"name":"api","path":"api","compute":"container","minInstances":3,"maxInstances":2}]}`)
	if err == nil {
		t.Fatal("Load admitted an app that runs at least 3 instances and at most 2")
	}
	for _, want := range []string{`app "api"`, "minInstances 3", "maxInstances 2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Load err = %q, want it to name %s", err, want)
		}
	}
}

func TestAnInstanceCountNoProviderCanHoldIsRefusedAtLoad(t *testing.T) {
	t.Parallel()

	_, err := loadJSON(t, `{"slug":"shop","apps":[{"name":"api","path":"api","compute":"container","maxInstances":4294967297}]}`)
	if err == nil {
		t.Fatal("Load admitted 4294967297 instances, which wraps to 1 on the wire")
	}
	for _, want := range []string{`app "api"`, "maxInstances", "2147483647"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Load err = %q, want it to name %s", err, want)
		}
	}
}
