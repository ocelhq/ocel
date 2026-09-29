package project

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/appbuild"
	"github.com/ocelhq/ocel/pkg/naming"
)

func TestAProjectNamingNoAppsHasTheNodeAppAtItsRootNamedAfterItsSlug(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{"slug":"shop"}`)
	write(t, filepath.Join(dir, nodeManifest), `{"dependencies":{"express":"5"}}`)

	cfg, err := Resolve(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.Apps) != 1 {
		t.Fatalf("Apps = %+v, want the one app at the project root", cfg.Apps)
	}
	if app := cfg.Apps[0]; app.Name != "shop" || app.Path != "." || app.Framework() != appbuild.FrameworkNode {
		t.Errorf("the root app = %+v, want a node app named %q at %q", app, "shop", ".")
	}
}

func TestTheRootAppOfANextProjectIsBuiltWithNext(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{"slug":"shop"}`)
	write(t, filepath.Join(dir, nodeManifest), `{"dependencies":{"next":"15"}}`)

	cfg, err := Resolve(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.Apps) != 1 || cfg.Apps[0].Framework() != appbuild.FrameworkNext {
		t.Errorf("Apps = %+v, want one next app", cfg.Apps)
	}
}

func TestAProjectWithNoPackageJSONAtItsRootAndNoAppsDeploysNoApp(t *testing.T) {
	t.Parallel()

	cfg := mustResolveJSON(t, `{"slug":"shop"}`)
	if len(cfg.Apps) != 0 {
		t.Errorf("Apps = %+v, want none: nothing at the root says an app is there", cfg.Apps)
	}
}

func TestAProjectWithNoConfigHasTheRootAppNamedAsInitWouldNameTheProject(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "My Shop")
	write(t, filepath.Join(dir, nodeManifest), `{}`)

	cfg, err := ResolveOptional(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("ResolveOptional: %v", err)
	}
	if len(cfg.Apps) != 1 || cfg.Apps[0].Name != "my-shop" {
		t.Errorf("Apps = %+v, want one app named %q", cfg.Apps, "my-shop")
	}
}

func TestAnAppTakingTheInfrastructureStacksNameIsRefusedAtLoad(t *testing.T) {
	t.Parallel()

	_, err := resolveJSON(t, `{"slug":"shop","apps":[{"name":"`+naming.InfraApp+`","path":"."}]}`)
	if err == nil || !strings.Contains(err.Error(), strconv.Quote(naming.InfraApp)+" is reserved") {
		t.Fatalf("Resolve err = %v, want the reserved name refused", err)
	}
}

func TestASlugTakingTheInfrastructureStacksNameIsRefusedWhenItWouldNameTheRootApp(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{"slug":"`+naming.InfraApp+`"}`)
	write(t, filepath.Join(dir, nodeManifest), `{}`)

	_, err := Resolve(context.Background(), dir, "")
	if err == nil || !strings.Contains(err.Error(), strconv.Quote(naming.InfraApp)+" is reserved") {
		t.Fatalf("Resolve err = %v, want the root app's reserved name refused", err)
	}
}

func TestAnAppDeclaringContainerComputeAndAFrameworkIsRefusedAtLoad(t *testing.T) {
	t.Parallel()

	_, err := resolveJSON(t, `{"slug":"shop","apps":[{"name":"api","path":"api","compute":"container","framework":"node"}]}`)
	if err == nil || !strings.Contains(err.Error(), "`framework`") {
		t.Fatalf("Resolve err = %v, want the framework refused on an app that runs the image it is given", err)
	}
}

func TestAnAppDeclaringServerlessComputeAndContainerConfigIsRefusedAtLoad(t *testing.T) {
	t.Parallel()

	for name, config := range map[string]string{
		"build":  `"build":{"dockerfile":"Dockerfile"}`,
		"health": `"health":{"path":"/healthz"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := resolveJSON(t, `{"slug":"shop","apps":[{"name":"api","path":"api","compute":"serverless","framework":"node",`+config+`}]}`)
			if err == nil || !strings.Contains(err.Error(), "`"+name+"`") {
				t.Fatalf("Resolve err = %v, want the %s refused on an app that runs as functions", err, name)
			}
		})
	}
}

func TestAnAppDeclaringAComputeHasOnlyThatComputesShape(t *testing.T) {
	t.Parallel()

	cfg := mustResolveJSON(t, `{"slug":"shop","apps":[
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
