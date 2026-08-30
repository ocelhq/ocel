package deploy

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func TestRefuseClaimedDomains(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		claims []*contractv1.DomainClaim
		refuse bool
	}{
		{
			name:   "no claims asked for",
			claims: nil,
		},
		{
			name:   "unclaimed passes",
			claims: []*contractv1.DomainClaim{{Hostname: "acme.com", Status: contractv1.DomainClaim_STATUS_UNCLAIMED}},
		},
		{
			name:   "unanswerable is skipped, never refused",
			claims: []*contractv1.DomainClaim{{Hostname: "acme.com", Status: contractv1.DomainClaim_STATUS_UNSPECIFIED}},
		},
		{
			name:   "claimed refuses",
			claims: []*contractv1.DomainClaim{{Hostname: "acme.com", Status: contractv1.DomainClaim_STATUS_CLAIMED, Owner: "ocel-other-preview"}},
			refuse: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := refuseClaimedDomains(tc.claims, projectconfig.ConfigFileName, func(string) {})
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

	t.Run("a hostname whose owner could not be read warns and deploys", func(t *testing.T) {
		t.Parallel()

		var warned []string
		err := refuseClaimedDomains([]*contractv1.DomainClaim{
			{Hostname: "acme.com", Cause: "the edge was throttled listing what it serves"},
		}, projectconfig.ConfigFileName, func(message string) { warned = append(warned, message) })
		if err != nil {
			t.Fatalf("refuseClaimedDomains err = %v, want a deploy that carries on when the provider could not say who serves the hostname", err)
		}
		if len(warned) != 1 || !strings.Contains(warned[0], "acme.com") || !strings.Contains(warned[0], "throttled") {
			t.Errorf("warnings = %q, want one naming the hostname and why its owner could not be read", warned)
		}
	})

	t.Run("every claimed hostname is named, and no unclaimed one", func(t *testing.T) {
		t.Parallel()

		err := refuseClaimedDomains([]*contractv1.DomainClaim{
			{Hostname: "acme.com", Status: contractv1.DomainClaim_STATUS_CLAIMED, Owner: "ocel-other-production-web"},
			{Hostname: "www.acme.com", Status: contractv1.DomainClaim_STATUS_UNCLAIMED},
			{Hostname: "shop.acme.com", Status: contractv1.DomainClaim_STATUS_CLAIMED, Owner: "ocel-third-production-web"},
		}, projectconfig.ConfigFileName, func(string) {})
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
	})
}

func TestDomainClaims(t *testing.T) {
	t.Run("a preview refuses a domain another project claims", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { preview: "*.preview.acme.com" },
};
`)
		deps := clitest.NewDeps()
		stubGit(&deps, "feature/login", "")
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(clitest.FakeDomainOwnerEnvVar, "ocel-other-preview")

		var stdout, stderr bytes.Buffer
		err := runPreviewUp(context.Background(), deps, root, previewUpOptions{}, &stdout, &stderr, strings.NewReader(""))
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
	})

	t.Run("a deploy refuses a domain another project claims", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { production: "acme.com" },
};
`)
		t.Setenv(clitest.FakeDomainOwnerEnvVar, "ocel-other-production-web")

		var stdout, stderr bytes.Buffer
		err := runDeploy(context.Background(), clitest.NewDeps(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
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
	})

	t.Run("a deploy declares the project's and the apps' hostnames", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { production: "acme.com" },
  apps: [{ name: "api", path: "apps/api", framework: "express", domains: { production: "api.acme.com" } }],
};
`)
		writeAppSource(t, root, "api")
		deps := clitest.NewDeps()
		clitest.StubBuild(&deps, nil)

		var stdout, stderr bytes.Buffer
		if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "PREFLIGHT slug=test-app domains=acme.com,api.acme.com") {
			t.Errorf("stdout = %q, want the declared hostnames to have reached Preflight", stdout.String())
		}
	})
}
