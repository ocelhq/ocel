package project

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
)

func TestAnAppThatNamesNoComputeTakesTheOneItsProviderNamesFirst(t *testing.T) {
	t.Parallel()

	cfg := &Project{Apps: []App{
		{Name: "api", Serverless: &Serverless{Framework: "node", Detected: true}, Container: &Container{}},
		{Name: "web", Serverless: &Serverless{Framework: "next", Detected: true}, Container: &Container{}},
	}}

	resolved, err := cfg.ResolveComputes([]string{"container", "serverless"}, "fake")
	if err != nil {
		t.Fatalf("ResolveComputes() error = %v, want the first compute resolved onto every app", err)
	}
	for _, app := range resolved.Apps {
		if app.Compute != provider.ComputeContainer {
			t.Errorf("app %q resolved to compute %q, want %q", app.Name, app.Compute, provider.ComputeContainer)
		}
	}
}

func TestResolvingComputesLeavesTheLoadedProjectAsDeclared(t *testing.T) {
	t.Parallel()

	cfg := &Project{Apps: []App{{Name: "api", Serverless: &Serverless{Framework: "node", Detected: true}, Container: &Container{}}}}

	resolved, err := cfg.ResolveComputes([]string{"container"}, "fake")
	if err != nil {
		t.Fatalf("ResolveComputes() error = %v", err)
	}
	if got := cfg.Apps[0]; got.Compute != "" || got.Serverless == nil {
		t.Errorf("the loaded app became %+v, want it left as the config declared it", got)
	}
	if got := resolved.Apps[0]; got.Compute != provider.ComputeContainer || got.Serverless != nil {
		t.Errorf("the resolved app = %+v, want a container with no framework", got)
	}
	if names := cfg.UnresolvedApps(); len(names) != 1 || names[0] != "api" {
		t.Errorf("UnresolvedApps() of the loaded project = %v, want [api]", names)
	}
	if names := resolved.UnresolvedApps(); len(names) != 0 {
		t.Errorf("UnresolvedApps() of the resolved project = %v, want none", names)
	}
}

func TestAnAppThatNamesAComputeItsProviderRunsKeepsIt(t *testing.T) {
	t.Parallel()

	cfg := &Project{Apps: []App{
		{Name: "api", Compute: provider.ComputeContainer, Container: &Container{}},
		{Name: "web", Serverless: &Serverless{Framework: "node", Detected: true}, Container: &Container{}},
	}}

	resolved, err := cfg.ResolveComputes([]string{"serverless", "container"}, "fake")
	if err != nil {
		t.Fatalf("ResolveComputes() error = %v, want a compute the provider runs admitted", err)
	}
	if resolved.Apps[0].Compute != provider.ComputeContainer {
		t.Errorf("app %q resolved to compute %q, want the %q it asked for", resolved.Apps[0].Name, resolved.Apps[0].Compute, provider.ComputeContainer)
	}
	if got := resolved.Apps[1]; got.Compute != provider.ComputeServerless || got.Container != nil {
		t.Errorf("app %q = %+v, want the provider's first compute and no container shape", got.Name, got)
	}
}

func TestAnAppThatNamesAComputeItsProviderDoesNotRunFailsThePlanByName(t *testing.T) {
	t.Parallel()

	cfg := &Project{Apps: []App{{Name: "api", Compute: provider.ComputeContainer, Container: &Container{}}}}

	_, err := cfg.ResolveComputes([]string{"serverless"}, "fake")
	if err == nil {
		t.Fatal("ResolveComputes() admitted a compute the provider does not run, want the plan refused")
	}
	for _, want := range []string{`"api"`, `"container"`, "fake", "serverless"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ResolveComputes() error = %q, want it to name %s", err, want)
		}
	}
}

func TestAProviderThatNamesNoComputeFailsThePlanByName(t *testing.T) {
	t.Parallel()

	cfg := &Project{Apps: []App{{Name: "api"}}}

	_, err := cfg.ResolveComputes(nil, "fake")
	if err == nil {
		t.Fatal("ResolveComputes() accepted a provider that names no compute, want the plan refused rather than a serverless guess")
	}
	if !strings.Contains(err.Error(), "fake") {
		t.Errorf("ResolveComputes() error = %q, want it to name the provider", err)
	}
	if strings.Contains(err.Error(), "serverless") {
		t.Errorf("ResolveComputes() error = %q, want no compute named where the provider named none", err)
	}
}

func TestAProviderNamingAComputeOcelDoesNotKnowFailsThePlanByName(t *testing.T) {
	t.Parallel()

	cfg := &Project{Apps: []App{{Name: "api"}}}

	_, err := cfg.ResolveComputes([]string{"vm"}, "fake")
	if err == nil {
		t.Fatal("ResolveComputes() stamped a compute ocel does not know onto every app, want the plan refused before the manifest is built")
	}
	for _, want := range []string{"fake", `"vm"`, `"serverless"`, `"container"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ResolveComputes() error = %q, want it to name %s", err, want)
		}
	}
}

func TestAServerlessAppThatConfiguresABuildFailsThePlanByName(t *testing.T) {
	t.Parallel()

	cfg := &Project{Apps: []App{
		{Name: "api", Serverless: &Serverless{Framework: "node"}, Container: &Container{Build: &Build{Dockerfile: "Dockerfile"}}},
	}}

	_, err := cfg.ResolveComputes([]string{"serverless"}, "fake")
	if err == nil {
		t.Fatal("ResolveComputes() admitted a build on an app its provider runs serverless, which builds no image")
	}
	for _, want := range []string{`"api"`, "build", "container"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ResolveComputes() error = %q, want it to name %s", err, want)
		}
	}
}

func TestAServerlessAppThatConfiguresAHealthCheckFailsThePlanByName(t *testing.T) {
	t.Parallel()

	cfg := &Project{Apps: []App{
		{Name: "api", Serverless: &Serverless{Framework: "node"}, Container: &Container{Health: &Health{Path: "/healthz"}}},
	}}

	_, err := cfg.ResolveComputes([]string{"serverless"}, "fake")
	if err == nil {
		t.Fatal("ResolveComputes() admitted a health check on an app its provider runs serverless, which has no process to probe")
	}
	for _, want := range []string{`"api"`, "health", "container"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ResolveComputes() error = %q, want it to name %s", err, want)
		}
	}
}

func TestAServerlessAppThatNamesInstanceCountsFailsThePlanByName(t *testing.T) {
	t.Parallel()

	three := 3
	cfg := &Project{Apps: []App{
		{Name: "api", Serverless: &Serverless{Framework: "node"}, Container: &Container{MaxInstances: &three}},
	}}

	_, err := cfg.ResolveComputes([]string{"serverless"}, "fake")
	if err == nil {
		t.Fatal("ResolveComputes() admitted instance counts on an app its provider runs serverless, which scales itself")
	}
	for _, want := range []string{`"api"`, "`maxInstances`", "container"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ResolveComputes() error = %q, want it to name %s", err, want)
		}
	}
}

func TestAnAppThatFallsBackToContainerIsRefusedTheFrameworkItNames(t *testing.T) {
	t.Parallel()

	cfg := &Project{Apps: []App{{Name: "api", Serverless: &Serverless{Framework: "node"}, Container: &Container{}}}}

	_, err := cfg.ResolveComputes([]string{"container"}, "fake")
	if err == nil {
		t.Fatal("ResolveComputes() admitted a framework on an app its provider runs in a container, which runs the image it is given")
	}
	for _, want := range []string{`"api"`, "`framework`", "serverless"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ResolveComputes() error = %q, want it to name %s", err, want)
		}
	}
}

func TestAnAppWhoseFrameworkWasOnlyDetectedKeepsItOnServerless(t *testing.T) {
	t.Parallel()

	cfg := &Project{Apps: []App{{Name: "web", Serverless: &Serverless{Framework: "next", Detected: true}, Container: &Container{}}}}

	resolved, err := cfg.ResolveComputes([]string{"serverless", "container"}, "fake")
	if err != nil {
		t.Fatalf("ResolveComputes() error = %v", err)
	}
	if got := resolved.Apps[0].Framework(); got != "next" {
		t.Errorf("framework = %q, want next: serverless builds the app with the framework it detected", got)
	}
}

func TestAnAppWhoseFrameworkIsUndetectedRunsOnAContainerOnlyProvider(t *testing.T) {
	t.Parallel()

	cfg := &Project{Apps: []App{{Name: "web", Container: &Container{}, undetected: errors.New(`app "web": nothing says what it is built with`)}}}

	resolved, err := cfg.ResolveComputes([]string{"container"}, "fake")
	if err != nil {
		t.Fatalf("ResolveComputes() error = %v, want a container app to need no framework", err)
	}
	if got := resolved.Apps[0]; got.Serverless != nil || got.Container == nil {
		t.Errorf("app = %+v, want the container shape alone", got)
	}
}

func TestAnAppWhoseFrameworkIsUndetectedIsRefusedServerless(t *testing.T) {
	t.Parallel()

	undetected := errors.New(`app "web": nothing says what it is built with`)
	cfg := &Project{Apps: []App{{Name: "web", Container: &Container{}, undetected: undetected}}}

	if _, err := cfg.ResolveComputes([]string{"serverless", "container"}, "fake"); !errors.Is(err, undetected) {
		t.Fatalf("ResolveComputes() error = %v, want %v: serverless builds an app's functions with its framework", err, undetected)
	}
}

func TestEveryAppNamingItsComputeResolvesWithoutAProvider(t *testing.T) {
	t.Parallel()

	cfg := &Project{Apps: []App{
		{Name: "api", Compute: provider.ComputeContainer, Container: &Container{}},
		{Name: "web", Compute: provider.ComputeServerless, Serverless: &Serverless{Framework: "node"}},
	}}

	resolved, err := cfg.ResolveDeclaredComputes()
	if err != nil {
		t.Fatalf("ResolveDeclaredComputes() error = %v", err)
	}
	if resolved.Apps[0].Compute != provider.ComputeContainer || resolved.Apps[1].Compute != provider.ComputeServerless {
		t.Errorf("resolved apps = %+v, want each on the compute it names", resolved.Apps)
	}
}

func TestAnAppNamingNoComputeCannotBeResolvedWithoutAProvider(t *testing.T) {
	t.Parallel()

	cfg := &Project{Apps: []App{{Name: "web", Serverless: &Serverless{Framework: "node", Detected: true}, Container: &Container{}}}}

	_, err := cfg.ResolveDeclaredComputes()
	if err == nil || !strings.Contains(err.Error(), `"web"`) || !strings.Contains(err.Error(), "provider") {
		t.Fatalf("ResolveDeclaredComputes() error = %v, want the app named and the provider it needs", err)
	}
}

func TestANextAppResolvedOntoContainerComputeKeepsItsFramework(t *testing.T) {
	t.Parallel()

	cfg := &Project{Apps: []App{{Name: "web", Serverless: &Serverless{Framework: "next", Detected: true}, Container: &Container{}}}}

	resolved, err := cfg.ResolveComputes([]string{"container"}, "fake")
	if err != nil {
		t.Fatalf("ResolveComputes() error = %v", err)
	}
	got := resolved.Apps[0]
	if got.Compute != provider.ComputeContainer || got.Serverless != nil || got.Framework() != "next" || got.Container == nil || got.Container.Framework != "next" {
		t.Errorf("the resolved app = %+v, want a container that keeps next", got)
	}
}

func TestANextAppDeclaredOnAContainerOnlyProviderIsAdmitted(t *testing.T) {
	t.Parallel()

	cfg := &Project{Apps: []App{{Name: "web", Serverless: &Serverless{Framework: "next"}, Container: &Container{}}}}

	resolved, err := cfg.ResolveComputes([]string{"container"}, "fake")
	if err != nil {
		t.Fatalf("ResolveComputes() error = %v, want a declared next admitted on container compute", err)
	}
	if got := resolved.Apps[0].Framework(); got != "next" {
		t.Errorf("framework = %q, want next", got)
	}
}

func TestResolvingANextAppOntoContainerLeavesTheLoadedContainerAsDeclared(t *testing.T) {
	t.Parallel()

	cfg := &Project{Apps: []App{{Name: "web", Serverless: &Serverless{Framework: "next", Detected: true}, Container: &Container{}}}}

	if _, err := cfg.ResolveComputes([]string{"container"}, "fake"); err != nil {
		t.Fatalf("ResolveComputes() error = %v", err)
	}
	if got := cfg.Apps[0]; got.Serverless == nil || got.Container.Framework != "" {
		t.Errorf("the loaded app became %+v, want it left as the config declared it", got)
	}
}
