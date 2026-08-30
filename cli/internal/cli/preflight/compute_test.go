package preflight

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func TestAnAppThatNamesNoComputeTakesTheOneItsProviderNamesFirst(t *testing.T) {
	t.Parallel()

	cfg := &projectconfig.Config{Apps: []projectconfig.App{{Name: "api"}, {Name: "web"}}}

	fallback, err := ResolveComputes(cfg, []string{"container", "serverless"}, "vps")
	if err != nil {
		t.Fatalf("ResolveComputes() error = %v, want the first compute resolved onto every app", err)
	}
	if fallback != "container" {
		t.Errorf("ResolveComputes() = %q, want the provider's first compute", fallback)
	}
	for _, app := range cfg.Apps {
		if app.Compute != "container" {
			t.Errorf("app %q resolved to compute %q, want %q", app.Name, app.Compute, "container")
		}
	}
}

func TestAnAppThatNamesAComputeItsProviderRunsKeepsIt(t *testing.T) {
	t.Parallel()

	cfg := &projectconfig.Config{Apps: []projectconfig.App{{Name: "api", Compute: "container"}, {Name: "web"}}}

	if _, err := ResolveComputes(cfg, []string{"serverless", "container"}, "fake"); err != nil {
		t.Fatalf("ResolveComputes() error = %v, want a compute the provider runs admitted", err)
	}
	if cfg.Apps[0].Compute != "container" {
		t.Errorf("app %q resolved to compute %q, want the %q it asked for", cfg.Apps[0].Name, cfg.Apps[0].Compute, "container")
	}
	if cfg.Apps[1].Compute != "serverless" {
		t.Errorf("app %q resolved to compute %q, want the provider's first", cfg.Apps[1].Name, cfg.Apps[1].Compute)
	}
}

func TestAnAppThatNamesAComputeItsProviderDoesNotRunFailsThePlanByName(t *testing.T) {
	t.Parallel()

	cfg := &projectconfig.Config{Apps: []projectconfig.App{{Name: "web"}, {Name: "api", Compute: "container"}}}

	_, err := ResolveComputes(cfg, []string{"serverless"}, "aws")
	if err == nil {
		t.Fatal("ResolveComputes() admitted a compute the provider does not run, want the plan refused")
	}
	for _, want := range []string{`"api"`, `"container"`, "aws", "serverless"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ResolveComputes() error = %q, want it to name %s", err, want)
		}
	}
}

func TestAProviderThatNamesNoComputeFailsThePlanByName(t *testing.T) {
	t.Parallel()

	cfg := &projectconfig.Config{Apps: []projectconfig.App{{Name: "api"}}}

	_, err := ResolveComputes(cfg, nil, "vps")
	if err == nil {
		t.Fatal("ResolveComputes() accepted a provider that names no compute, want the plan refused rather than a serverless guess")
	}
	if !strings.Contains(err.Error(), "vps") {
		t.Errorf("ResolveComputes() error = %q, want it to name the provider", err)
	}
	if strings.Contains(err.Error(), "serverless") {
		t.Errorf("ResolveComputes() error = %q, want no compute named where the provider named none", err)
	}
}

func TestAProviderNamingAComputeOcelDoesNotKnowFailsThePlanByName(t *testing.T) {
	t.Parallel()

	cfg := &projectconfig.Config{Apps: []projectconfig.App{{Name: "api"}}}

	_, err := ResolveComputes(cfg, []string{"vm"}, "@ocel/provider-vps")
	if err == nil {
		t.Fatal("ResolveComputes() stamped a compute ocel does not know onto every app, want the plan refused before the manifest is built")
	}
	for _, want := range []string{"@ocel/provider-vps", `"vm"`, `"serverless"`, `"container"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ResolveComputes() error = %q, want it to name %s", err, want)
		}
	}
	if cfg.Apps[0].Compute != "" {
		t.Errorf("app %q resolved to compute %q, want it left alone on the failure path", cfg.Apps[0].Name, cfg.Apps[0].Compute)
	}
}

func TestARefusedPlanLeavesNoAppHalfResolved(t *testing.T) {
	t.Parallel()

	cfg := &projectconfig.Config{Apps: []projectconfig.App{{Name: "web"}, {Name: "api", Compute: "container"}}}

	if _, err := ResolveComputes(cfg, []string{"serverless"}, "aws"); err == nil {
		t.Fatal("ResolveComputes() admitted a compute the provider does not run, want the plan refused")
	}
	if cfg.Apps[0].Compute != "" {
		t.Errorf("app %q resolved to compute %q, want the apps before the offending one left alone", cfg.Apps[0].Name, cfg.Apps[0].Compute)
	}
}

func TestAProviderWhoseIdentityIsEmptyIsStillNamedByThePlan(t *testing.T) {
	t.Parallel()

	cfg := &projectconfig.Config{Apps: []projectconfig.App{{Name: "api"}}}

	_, err := ResolveComputes(cfg, nil, "@ocel/provider-aws")
	if err == nil {
		t.Fatal("ResolveComputes() accepted a provider that names no compute, want the plan refused")
	}
	if !strings.Contains(err.Error(), "@ocel/provider-aws") {
		t.Errorf("ResolveComputes() error = %q, want the provider named from the package the project pins, which stands whether or not preflight could answer an identity", err)
	}
}

func TestAServerlessAppThatConfiguresABuildFailsThePlanByName(t *testing.T) {
	t.Parallel()

	cfg := &projectconfig.Config{Apps: []projectconfig.App{
		{Name: "web"},
		{Name: "api", Compute: "serverless", Build: &projectconfig.Build{Dockerfile: "Dockerfile"}},
	}}

	_, err := ResolveComputes(cfg, []string{"serverless", "container"}, "aws")
	if err == nil {
		t.Fatal("ResolveComputes() admitted a build on a serverless app, which builds no image, so config that can do nothing would look like it might")
	}
	for _, want := range []string{`"api"`, "build", "container"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ResolveComputes() error = %q, want it to name %s", err, want)
		}
	}
	if cfg.Apps[0].Compute != "" {
		t.Errorf("app %q resolved to compute %q, want the apps before the offending one left alone", cfg.Apps[0].Name, cfg.Apps[0].Compute)
	}
}

func TestAnAppThatFallsBackToServerlessIsRefusedItsBuildToo(t *testing.T) {
	t.Parallel()

	cfg := &projectconfig.Config{Apps: []projectconfig.App{
		{Name: "api", Build: &projectconfig.Build{Dockerfile: "Dockerfile"}},
	}}

	if _, err := ResolveComputes(cfg, []string{"serverless"}, "aws"); err == nil {
		t.Fatal("ResolveComputes() admitted a build on an app its provider runs serverless, so the refusal turns on what the config says rather than what the app runs on")
	}
}

func TestAServerlessAppThatConfiguresAHealthCheckFailsThePlanByName(t *testing.T) {
	t.Parallel()

	cfg := &projectconfig.Config{Apps: []projectconfig.App{
		{Name: "web"},
		{Name: "api", Compute: "serverless", Health: &projectconfig.Health{Path: "/healthz"}},
	}}

	_, err := ResolveComputes(cfg, []string{"serverless", "container"}, "aws")
	if err == nil {
		t.Fatal("ResolveComputes() admitted a health check on a serverless app, which has no always-on process to probe, so config that can do nothing would look like it might")
	}
	for _, want := range []string{`"api"`, "health", "container"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ResolveComputes() error = %q, want it to name %s", err, want)
		}
	}
	if cfg.Apps[0].Compute != "" {
		t.Errorf("app %q resolved to compute %q, want the apps before the offending one left alone", cfg.Apps[0].Name, cfg.Apps[0].Compute)
	}
}

func TestAnAppThatFallsBackToServerlessIsRefusedItsHealthCheckToo(t *testing.T) {
	t.Parallel()

	cfg := &projectconfig.Config{Apps: []projectconfig.App{
		{Name: "api", Health: &projectconfig.Health{Path: "/healthz"}},
	}}

	if _, err := ResolveComputes(cfg, []string{"serverless"}, "aws"); err == nil {
		t.Fatal("ResolveComputes() admitted a health check on an app its provider runs serverless, so the refusal turns on what the config says rather than what the app runs on")
	}
}

func TestAContainerAppKeepsItsHealthCheck(t *testing.T) {
	t.Parallel()

	cfg := &projectconfig.Config{Apps: []projectconfig.App{
		{Name: "api", Compute: "container", Health: &projectconfig.Health{Path: "/healthz"}},
	}}

	if _, err := ResolveComputes(cfg, []string{"serverless", "container"}, "vps"); err != nil {
		t.Errorf("ResolveComputes() = %v, want a health check admitted on the compute it configures", err)
	}
}

func TestAContainerAppKeepsItsBuild(t *testing.T) {
	t.Parallel()

	cfg := &projectconfig.Config{Apps: []projectconfig.App{
		{Name: "api", Compute: "container", Build: &projectconfig.Build{Dockerfile: "Dockerfile"}},
	}}

	if _, err := ResolveComputes(cfg, []string{"serverless", "container"}, "vps"); err != nil {
		t.Errorf("ResolveComputes() = %v, want a build admitted on the compute it configures", err)
	}
}
