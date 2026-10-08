package deploy

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/deployreport"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/environment"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/statedir"
)

func TestDeployRefusesBeforeStartingAProviderWhenTheConfigCannotNameOne(t *testing.T) {
	t.Run("a missing config errors before any spawn", func(t *testing.T) {
		err := runDeploy(context.Background(), newTestDependencies(), t.TempDir(), deployOptions{yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want error")
		}
		if !strings.Contains(err.Error(), "ocel init") {
			t.Fatalf("err = %v, want it to hint at `ocel init`", err)
		}
	})

	t.Run("a malformed config errors before any spawn", func(t *testing.T) {
		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `this is not valid TypeScript {{{`)

		err := runDeploy(context.Background(), newTestDependencies(), root, deployOptions{yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want error")
		}
		if !strings.Contains(err.Error(), "ocel.config.ts") {
			t.Fatalf("err = %v, want it to mention ocel.config.ts", err)
		}
	})

	t.Run("no provider configured errors before any spawn", func(t *testing.T) {
		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
};
`)

		err := runDeploy(context.Background(), newTestDependencies(), root, deployOptions{yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want error")
		}
		if !strings.Contains(err.Error(), "provider") {
			t.Fatalf("err = %v, want it to mention the missing provider", err)
		}
	})
}

func TestADeployStreamsTheProvidersProgressAndPromotesProduction(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	fixture := setUpDeployProject(t)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	out := stdout.String()

	t.Run("streams the provider's progress", func(t *testing.T) {
		if !strings.Contains(out, "Provisioned stack") {
			t.Errorf("stdout = %q, want the provider's streamed progress", out)
		}
	})
	t.Run("ends on a terminal success message", func(t *testing.T) {
		if !strings.Contains(out, "Deployed test-app to production") {
			t.Errorf("stdout = %q, want a terminal success message", out)
		}
	})
	t.Run("sends a production environment", func(t *testing.T) {
		env := sentDeploy(t, fixture).GetEnvironment()
		if env.GetTier() != environmentv1.Tier_TIER_PRODUCTION || env.GetLifecycle() != environmentv1.Lifecycle_LIFECYCLE_UNSPECIFIED {
			t.Errorf("deploy environment = %v, want production", env)
		}
	})
	t.Run("promotes production", func(t *testing.T) {
		if promoted := activePromotion(t, fixture, environment.TierProduction, router.DefaultPointer); promoted == "" {
			t.Error("production serves no promotion after the deploy")
		}
	})
	t.Run("skips the confirm prompt under --yes", func(t *testing.T) {
		if strings.Contains(out, "[y/N]") {
			t.Errorf("stdout = %q, want the confirm prompt skipped by --yes", out)
		}
	})
}

func TestADeployWhoseStdinIsNotATerminalProceedsWithoutPrompting(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	fixture := setUpDeployProject(t)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "[y/N]") {
		t.Errorf("stdout = %q, want the confirm prompt skipped for non-TTY stdin", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Deployed") {
		t.Errorf("stdout = %q, want deploy to still proceed to success", stdout.String())
	}
}

func TestADeploysResultNamesTheProjectAndProduction(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	useJSONFormat(t, &dependencies)
	fixture := setUpDeployProject(t)

	var stream, stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stream)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stream=%s stderr=%s", err, stream.String(), stderr.String())
	}

	evs := envelopes(t, stream.String())
	if headline := evs[len(evs)-1].GetSummary().GetHeadline(); headline != "Deployed test-app to production" {
		t.Fatalf("result headline = %q, want it to name the project and production", headline)
	}
}

func activePromotion(t *testing.T, fixture clitest.FakeProject, tier environment.Tier, pointer string) string {
	t.Helper()
	promoted, err := fixture.Provider.Releases(tier, clitest.FixtureSlug).ActivePromotionID(context.Background(), pointer)
	if err != nil {
		t.Fatalf("read the promotion %s serves: %v", pointer, err)
	}
	return promoted
}

func TestADeployRecordsWhatItDeployed(t *testing.T) {
	t.Run("a successful deploy records the promotion, the tag and every app", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, apiFunction())
		fixture := setUpDeployProject(t)
		writeConfig(t, fixture.Root, `  apps: [{ name: "api", path: "apps/api", framework: "node" }],
  dns: { zone: { zone: "acme.com" } },
`)
		writeAppSource(t, fixture.Root, "api")
		writeHosting(t, fixture.Root, "api", "bld_api_1")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true, tag: "v9"}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		got := readDeployReport(t, fixture.Root)
		if got.GetEnvironment().GetTier() != environmentv1.Tier_TIER_PRODUCTION {
			t.Errorf("environment.tier = %v, want production", got.GetEnvironment().GetTier())
		}
		if got.GetProvider().GetName() != "fake" {
			t.Errorf("provider = %v, want the config's provider", got.GetProvider())
		}
		if want := activePromotion(t, fixture, environment.TierProduction, router.DefaultPointer); want == "" || got.GetPromotion().GetId() != want {
			t.Errorf("promotion = %q, want the %q production now serves", got.GetPromotion().GetId(), want)
		}
		if got.GetPromotion().GetTag() != "v9" {
			t.Errorf("tag = %q, want %q", got.GetPromotion().GetTag(), "v9")
		}
		if len(got.GetApps()) != 1 || got.GetApps()[0].GetName() != "api" || got.GetApps()[0].GetFrameworkBuildId() != "bld_api_1" {
			t.Errorf("apps = %v, want one api app with framework build id bld_api_1", got.GetApps())
		}
		if len(got.GetApps()) == 1 && !slices.Contains(got.GetApps()[0].GetUrls(), "https://"+productionDomain) {
			t.Errorf("apps = %v, want api's URLs to include the hostname it serves on", got.GetApps())
		}
		if got.GetFinishedAt().AsTime().IsZero() {
			t.Error("finishedAt is zero, want the completion time")
		}
	})

	t.Run("a failed deploy leaves no stale result behind", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, nil)
		fixture := setUpDeployProject(t)
		if err := deployreport.Write(fixture.Root, &consolev1.Deployment{Id: "4bf92f3577b34da6a3ce929d0e0e4736"}); err != nil {
			t.Fatalf("seed stale result: %v", err)
		}
		fixture.Provider.FakeStacks().Entering(func(provider.StackSpec) error { return errors.New("simulated deploy failure") })

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatalf("runDeploy err = nil, want the simulated failure; stdout=%s", stdout.String())
		}

		if _, statErr := os.Stat(deployreport.Path(fixture.Root)); !errors.Is(statErr, fs.ErrNotExist) {
			t.Errorf("stat %s = %v, want no result file after a failed deploy", deployreport.Path(fixture.Root), statErr)
		}
	})

	t.Run("a successful preview up records the named preview", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, apiFunction())
		fixture := setUpPreviewProject(t)
		addAppToFixtureConfig(t, fixture.Root)
		writeHosting(t, fixture.Root, "api", "bld_api_1")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runPreviewUp(context.Background(), dependencies, fixture.Root, previewUpOptions{name: "e2e-42", persistent: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runPreviewUp err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		got := readDeployReport(t, fixture.Root)
		if got.GetKind() != consolev1.DeploymentKind_DEPLOYMENT_KIND_PREVIEW_UP || got.GetEnvironment().GetTier() != environmentv1.Tier_TIER_PREVIEW || got.GetEnvironment().GetIdentity() != "e2e-42" {
			t.Errorf("deployment is a %v in %v, want a preview up of the named preview", got.GetKind(), got.GetEnvironment())
		}
		if want := activePromotion(t, fixture, environment.TierPreview, "e2e-42"); want == "" || got.GetPromotion().GetId() != want {
			t.Errorf("promotion = %q, want the %q the preview now serves", got.GetPromotion().GetId(), want)
		}
		if len(got.GetApps()) != 1 || len(got.GetApps()[0].GetUrls()) == 0 {
			t.Errorf("apps = %v, want api with the URLs the preview serves it on", got.GetApps())
		}
	})
}

func writeHosting(t *testing.T, root, app, buildID string) {
	t.Helper()
	clitest.WriteFile(t, filepath.Join(root, statedir.Name, "output", "apps", app, buildoutput.HostingFile),
		`{"framework":"node","frameworkBuildId":"`+buildID+`"}`)
}

func setUpProviderProject(t *testing.T, options string, transforms string) (clitest.FakeProject, Dependencies) {
	t.Helper()

	fixture := setUpDeployProject(t)
	clitest.WriteUsageMonorepo(t, fixture.Root)
	clitest.WriteFile(t, filepath.Join(fixture.Root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  transforms: [`+transforms+`],
  provider: { fake: `+options+` },
  domains: { production: "`+productionDomain+`" },
  apps: [{ name: "api", path: "apps/api", framework: "node" }],
};
`)

	dependencies := newTestDependencies()
	stubBuild(&dependencies, apiFunction())
	return fixture, dependencies
}

func TestDeployConfiguresTheProviderOnceAtSessionSetup(t *testing.T) {
	fixture, dependencies := setUpProviderProject(t, `{ region: "zone-b" }`, `"./transforms/net.transform.ts"`)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil || !strings.Contains(stdout.String(), `lists "./transforms/net.transform.ts" under "transforms"`) {
		t.Fatalf("runDeploy err = %v; stdout=%s, want the provider, which renders nothing a transform patches, to refuse the transform it was configured with", err, stdout.String())
	}

	configured := clitest.RequestsTo[*contractv1.ConfigureRequest](t, fixture.Requests, contractv1connect.ProviderServiceConfigureProcedure)
	if len(configured) != 1 {
		t.Fatalf("the provider was configured %d times, want exactly 1 for the session", len(configured))
	}
	config := configured[0].GetConfig()
	if region := config.GetOptions().AsMap()["region"]; region != "zone-b" {
		t.Errorf("provider options = %v, want the region the config names", config.GetOptions().AsMap())
	}
	if !slices.Equal(config.GetTransforms(), []string{"./transforms/net.transform.ts"}) {
		t.Errorf("provider transforms = %v, want the one the config names", config.GetTransforms())
	}
}

func TestDeployRendersTheProviderRefusalAgainstTheConfigFile(t *testing.T) {
	fixture, dependencies := setUpProviderProject(t, `{ regionn: "zone-b" }`, "")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatalf("runDeploy err = nil, want options the provider refuses reported; stdout=%s", stdout.String())
	}
	rendered := stdout.String() + stderr.String()
	for _, want := range []string{
		`configures provider "fake" with options it does not accept`,
		`"provider.fake.regionn"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered output = %q, want it to contain %q", rendered, want)
		}
	}
	if strings.Contains(rendered, "invalid_argument:") {
		t.Errorf("rendered output = %q, want no raw connect code prefix", rendered)
	}
}

func deployUsageMonorepo(t *testing.T, fields string) (clitest.FakeProject, string, error) {
	t.Helper()
	fixture := setUpDeployProject(t)
	writeUsageMonorepo(t, fixture.Root, fields)
	dependencies := newTestDependencies()
	stubBuild(&dependencies, apiFunction())

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	return fixture, stdout.String() + stderr.String(), err
}

func TestDeploySendsTheEdgeTheProjectDeclared(t *testing.T) {
	cases := []struct {
		name        string
		declaration string
		want        string
	}{
		{"an omitted edge names none, leaving the provider to choose", "", ""},
		{"a declared direct edge names it", "  edge: \"direct\",\n", "direct"},
		{"a declared relay edge names it", "  edge: \"relay\",\n", "relay"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture, out, err := deployUsageMonorepo(t, tc.declaration)
			if err != nil {
				t.Fatalf("runDeploy err = %v; output=%s", err, out)
			}
			if got := sentDeploy(t, fixture).GetEdge().GetKind(); got != tc.want {
				t.Errorf("the deploy named edge %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDeploySendsTheEdgeSettingsUnchanged(t *testing.T) {
	fixture, out, err := deployUsageMonorepo(t, "  edge: \"relay\",\n  dns: { zone: { zone: \"acme.com\" } },\n  allowDegraded: [\"streaming\", \"edge-cache\"],\n")
	if err != nil {
		t.Fatalf("runDeploy err = %v; output=%s", err, out)
	}

	sent := sentDeploy(t, fixture).GetEdge()
	if sent.GetDns().GetKind() != string(fake.KindZone) || sent.GetDns().GetZone() != "acme.com" {
		t.Errorf("the deploy named DNS %s/%s, want zone/acme.com", sent.GetDns().GetKind(), sent.GetDns().GetZone())
	}
	if !slices.Equal(sent.GetAllowDegraded(), []string{"streaming", "edge-cache"}) {
		t.Errorf("the deploy allowed %v degraded, want streaming and edge-cache", sent.GetAllowDegraded())
	}
}

func TestDeployRendersAnEdgeTheOriginRefuses(t *testing.T) {
	const refusal = `this provider cannot front deployments with the "relay" edge today; it supports direct`

	fixture := setUpDeployProject(t)
	writeUsageMonorepo(t, fixture.Root, "")
	fixture.Provider.Edges().(*fake.Edges).Edge(fake.KindRelay).Refuse(errors.New(refusal))
	dependencies := newTestDependencies()
	stubBuild(&dependencies, apiFunction())

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatalf("runDeploy err = nil, want the refused edge to fail the deploy; stdout=%s stderr=%s", stdout.String(), stderr.String())
	}

	rendered := stdout.String() + stderr.String()
	if !strings.Contains(rendered, refusal) {
		t.Errorf("rendered output = %q, want it to include %q", rendered, refusal)
	}
	if strings.Contains(rendered, "connection lost") {
		t.Errorf("rendered output = %q, want a refusal not to read as a lost connection", rendered)
	}
}
