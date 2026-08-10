package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func TestDeclaredHostnames(t *testing.T) {
	cfg := &projectconfig.Config{
		Domains: map[string][]string{
			"production": {"acme.com", "www.acme.com"},
			"preview":    {"*.preview.acme.com"},
		},
		Apps: []projectconfig.App{
			{Name: "web", Domains: map[string][]string{"production": {"app.acme.com", "acme.com"}}},
			{Name: "api", Domains: map[string][]string{"production": {"api.acme.com"}}},
			{Name: "admin"},
		},
	}

	got := declaredHostnames(cfg, "production")
	want := []string{"acme.com", "www.acme.com", "app.acme.com", "api.acme.com"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("declaredHostnames(production) = %v, want %v (declared order, deduped)", got, want)
	}

	// The preview wildcard travels with its literal leading "*.": the provider
	// forms the route pattern from it without inferring the class.
	if got := declaredHostnames(cfg, "preview"); len(got) != 1 || got[0] != "*.preview.acme.com" {
		t.Errorf("declaredHostnames(preview) = %v, want [*.preview.acme.com]", got)
	}

	if got := declaredHostnames(&projectconfig.Config{}, "production"); len(got) != 0 {
		t.Errorf("declaredHostnames of a domain-less project = %v, want none", got)
	}
}

func TestRefuseClaimedDomains(t *testing.T) {
	cases := []struct {
		name   string
		claims []*deploymentsv1.DomainClaim
		refuse bool
	}{
		{
			name:   "no claims asked for",
			claims: nil,
		},
		{
			name:   "unclaimed passes",
			claims: []*deploymentsv1.DomainClaim{{Hostname: "acme.com", Status: deploymentsv1.DomainClaim_STATUS_UNCLAIMED}},
		},
		{
			// An edge that cannot answer must never fail a deploy: the guard
			// degrades to the late collision it already survives.
			name:   "unanswerable is skipped, never refused",
			claims: []*deploymentsv1.DomainClaim{{Hostname: "acme.com", Status: deploymentsv1.DomainClaim_STATUS_UNSPECIFIED}},
		},
		{
			// A hostname this project's own workers hold is reported
			// unclaimed, so claimed is always another project's.
			name:   "claimed refuses",
			claims: []*deploymentsv1.DomainClaim{{Hostname: "acme.com", Status: deploymentsv1.DomainClaim_STATUS_CLAIMED, Owner: "ocel-other-preview"}},
			refuse: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := refuseClaimedDomains(tc.claims)
			if !tc.refuse {
				if err != nil {
					t.Fatalf("refuseClaimedDomains err = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("refuseClaimedDomains err = nil, want a refusal")
			}
			for _, want := range []string{"acme.com", tc.claims[0].GetOwner()} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v, want it to name %q", err, want)
				}
			}
		})
	}
}

func TestRefuseClaimedDomains_NamesEveryClaimedHostname(t *testing.T) {
	err := refuseClaimedDomains([]*deploymentsv1.DomainClaim{
		{Hostname: "acme.com", Status: deploymentsv1.DomainClaim_STATUS_CLAIMED, Owner: "ocel-other-production-web"},
		{Hostname: "www.acme.com", Status: deploymentsv1.DomainClaim_STATUS_UNCLAIMED},
		{Hostname: "shop.acme.com", Status: deploymentsv1.DomainClaim_STATUS_CLAIMED, Owner: "ocel-third-production-web"},
	})
	if err == nil {
		t.Fatal("refuseClaimedDomains err = nil, want a refusal")
	}
	for _, want := range []string{"acme.com", "ocel-other-production-web", "shop.acme.com", "ocel-third-production-web"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "www.acme.com") {
		t.Errorf("err = %v, want the unclaimed hostname left out", err)
	}
}

// TestRunPreviewUp_RefusesDomainClaimedByAnotherProject is the point of the
// feature: a wildcard another project holds costs the user nothing — the
// refusal lands before the build, so nothing is built and nothing is stranded.
func TestRunPreviewUp_RefusesDomainClaimedByAnotherProject(t *testing.T) {
	root, _ := setUpDeployFixture(t)
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { preview: "*.preview.acme.com" },
};
`)
	stubGit(t, "feature/login", "")
	t.Setenv(fakeInfraClassEnvVar, "preview")
	t.Setenv(fakeInfraPresentEnvVar, "1")
	t.Setenv(fakeDomainOwnerEnvVar, "ocel-other-preview")

	var stdout, stderr bytes.Buffer
	err := runPreviewUp(context.Background(), root, previewUpOptions{}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("runPreviewUp err = nil, want a domain-claim refusal")
	}

	out := stdout.String()
	for _, want := range []string{"*.preview.acme.com", "ocel-other-preview"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want it to name %q", out, want)
		}
	}
	if strings.Contains(out, "Building project") {
		t.Errorf("stdout = %q, want the refusal before anything is built", out)
	}
	if strings.Contains(out, "DEPLOY ") {
		t.Errorf("stdout = %q, want no Deploy to have been driven", out)
	}
}

func TestRunDeploy_RefusesDomainClaimedByAnotherProject(t *testing.T) {
	root, _ := setUpDeployFixture(t)
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { production: "acme.com" },
};
`)
	t.Setenv(fakeDomainOwnerEnvVar, "ocel-other-production-web")

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("runDeploy err = nil, want a domain-claim refusal")
	}

	out := stdout.String()
	for _, want := range []string{"acme.com", "ocel-other-production-web"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want it to name %q", out, want)
		}
	}
	if strings.Contains(out, "Building project") {
		t.Errorf("stdout = %q, want the refusal before anything is built", out)
	}
	if strings.Contains(out, "DEPLOY ") {
		t.Errorf("stdout = %q, want no Deploy to have been driven", out)
	}
}

// TestRunDeploy_DeclaresProjectAndAppHostnames proves the hostnames the claim
// check is asked about come from the config before the build, project-level and
// per-app alike.
func TestRunDeploy_DeclaresProjectAndAppHostnames(t *testing.T) {
	root, _ := setUpDeployFixture(t)
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { production: "acme.com" },
  apps: [{ name: "api", path: "apps/api", framework: "express", domains: { production: "api.acme.com" } }],
};
`)
	stubAppFunctions(t, nil)

	var stdout, stderr bytes.Buffer
	if err := runDeploy(context.Background(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "PREFLIGHT slug=test-app domains=acme.com,api.acme.com") {
		t.Errorf("stdout = %q, want the declared hostnames to have reached Preflight", stdout.String())
	}
}
