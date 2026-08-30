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

func TestThreeComputesReadAsAList(t *testing.T) {
	t.Parallel()

	if got, want := list([]string{"a", "b", "c"}), `"a", "b" and "c"`; got != want {
		t.Errorf("list() = %s, want %s", got, want)
	}
	if got, want := list([]string{"a", "b"}), `"a" and "b"`; got != want {
		t.Errorf("list() = %s, want %s", got, want)
	}
	if got, want := list([]string{"a"}), `"a"`; got != want {
		t.Errorf("list() = %s, want %s", got, want)
	}
}
