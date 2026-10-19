package bootstrap

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

var production = []environmentv1.Tier{environmentv1.Tier_TIER_PRODUCTION}

func statusProject(t *testing.T) (clitest.FakeProject, commands.Invocation) {
	t.Helper()
	project, invocation := bootstrapProject(t, "", partISR)
	ctx := context.Background()
	store := project.Provider.KeyValues()
	added := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	if err := stackrecords.MarkBootstrapPartReady(ctx, store, environment.TierProduction, provider.BasePart, stackrecords.BootstrapPart{AddedAt: added}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := stackrecords.ClaimBootstrapPart(ctx, store, environment.TierProduction, "containers", stackrecords.BootstrapPart{AddedBy: "api", AddedAt: added.AddDate(0, 1, 8)}); err != nil {
		t.Fatal(err)
	}
	project.Provider.FakeBootstrap().AddPart(environment.TierProduction, provider.BootstrapPart{
		Name:      "containers",
		UsedBy:    []string{"api"},
		Resources: []provider.PhysicalResource{{Kind: "Fake::Cluster", ID: "ocel-containers-production", Location: "nowhere-1", ConsoleURL: "https://console.fake.invalid/clusters/ocel-containers-production"}},
		Shape: &provider.PartShape{Vendor: string(fake.Vendor), Resources: []pricing.Shaped{
			{Name: "containers", Type: fake.TypeContainer, Properties: map[string]any{}},
		}},
	})
	return project, invocation
}

func runStatus(t *testing.T, invocation commands.Invocation, root string, tiers []environmentv1.Tier, opts StatusOptions) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stderr)
	if err := RunStatus(context.Background(), invocation, root, tiers, opts, &stdout); err != nil {
		t.Fatalf("RunStatus err = %v; stderr=%s", err, stderr.String())
	}
	return stdout.String()
}

func rowOf(t *testing.T, out, part string) []string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if fields := strings.Fields(line); len(fields) > 0 && fields[0] == part {
			return fields
		}
	}
	t.Fatalf("output has no row for %s:\n%s", part, out)
	return nil
}

func TestBootstrapStatusShowsOneRowPerPartWithWhoAddedItWhoUsesItItsIdleCostAndItsState(t *testing.T) {
	project, invocation := statusProject(t)

	out := runStatus(t, invocation, project.Root, production, StatusOptions{})

	if !strings.HasPrefix(out, "production") {
		t.Errorf("output = %q, want it headed by the tier", out)
	}
	for _, column := range []string{"PART", "ADDED", "USED BY", "IDLE", "STATE"} {
		if !strings.Contains(out, column) {
			t.Errorf("output has no %s column:\n%s", column, out)
		}
	}
	for part, want := range map[string]string{
		provider.BasePart: "base ocel bootstrap · 2026-09-02 — $0 current",
		partISR:           partISR + " — — $0 current",
		"containers":      "containers deploy api · 2026-10-10 api $7.30/mo creating",
	} {
		if got := strings.Join(rowOf(t, out, part), " "); got != want {
			t.Errorf("%s row = %q, want %q", part, got, want)
		}
	}
	if strings.Contains(out, "ocel-containers-production") {
		t.Errorf("output names a resource without --resources:\n%s", out)
	}
}

func TestBootstrapStatusSaysACommandAddedAPartRatherThanADeploy(t *testing.T) {
	project, invocation := statusProject(t)
	if err := stackrecords.EnsureBootstrapPart(context.Background(), project.Provider.KeyValues(), environment.TierProduction, stackrecords.PartWork{
		Name:      "edge-kit",
		WrittenBy: "1.2.3",
		AddedWith: "ocel domain use --preview",
		Make:      func(context.Context) ([]provider.PhysicalResource, error) { return nil, nil },
	}); err != nil {
		t.Fatal(err)
	}

	out := runStatus(t, invocation, project.Root, production, StatusOptions{})

	if got := strings.Join(rowOf(t, out, "edge-kit"), " "); !strings.HasPrefix(got, "edge-kit ocel domain use --preview · ") {
		t.Errorf("edge-kit row = %q, want it added by `ocel domain use --preview`, no deploy", got)
	}
}

func TestBootstrapStatusAsJSONSaysWhichCommandAddedAPart(t *testing.T) {
	project, invocation := statusProject(t)
	if err := stackrecords.EnsureBootstrapPart(context.Background(), project.Provider.KeyValues(), environment.TierProduction, stackrecords.PartWork{
		Name:      "edge-kit",
		WrittenBy: "1.2.3",
		AddedBy:   "web",
		AddedWith: "ocel domain use --preview",
		Make:      func(context.Context) ([]provider.PhysicalResource, error) { return nil, nil },
	}); err != nil {
		t.Fatal(err)
	}
	invocation.Presentation = func(io.Writer) terminal.Presentation {
		return terminal.Resolve(terminal.Conditions{Format: terminal.FormatJSON})
	}

	report := clitest.DecodeResult(t, runStatus(t, invocation, project.Root, production, StatusOptions{}))

	tiers, _ := report["tiers"].([]any)
	tier, _ := tiers[0].(map[string]any)
	parts, _ := tier["parts"].([]any)
	for _, each := range parts {
		part, _ := each.(map[string]any)
		if part["name"] != "edge-kit" {
			continue
		}
		if part["addedWith"] != "ocel domain use --preview" || part["addedBy"] != "web" {
			t.Errorf("json edge-kit = %v, want it added with `ocel domain use --preview` for web", part)
		}
		return
	}
	t.Fatalf("json parts = %v, want an edge-kit part", parts)
}

func TestBootstrapStatusSaysAPartWithAnIdleCostRetiresWithTheProjectsThatUseIt(t *testing.T) {
	project, invocation := statusProject(t)
	project.Provider.FakeBootstrap().AddPart(environment.TierProduction, provider.BootstrapPart{
		Name:                "balancer",
		Current:             true,
		UsedBy:              []string{"shop"},
		RetiresWithLastUser: true,
	})

	out := runStatus(t, invocation, project.Root, production, StatusOptions{})

	if got, want := strings.Join(rowOf(t, out, "balancer"), " "), "balancer — shop — retires with shop"; got != want {
		t.Errorf("balancer row = %q, want %q: the deploy or destroy that removes its last user takes it down", got, want)
	}
}

func TestBootstrapStatusSaysWhoWantsAPartTheBootstrapLacksAndTheCommandThatAddsIt(t *testing.T) {
	project, invocation := statusProject(t)
	for _, wanting := range []string{"web", "api"} {
		if err := stackrecords.RecordBootstrapWant(context.Background(), project.Provider.KeyValues(), environment.TierProduction, partImageOptimization, wanting); err != nil {
			t.Fatal(err)
		}
	}

	out := runStatus(t, invocation, project.Root, production, StatusOptions{})

	want := partImageOptimization + " — — — wanted by api, web, run `ocel bootstrap production`"
	if got := strings.Join(rowOf(t, out, partImageOptimization), " "); got != want {
		t.Errorf("%s row = %q, want %q", partImageOptimization, got, want)
	}
}

func TestBootstrapStatusWithResourcesListsWhatEachPartHolds(t *testing.T) {
	project, invocation := statusProject(t)

	out := runStatus(t, invocation, project.Root, production, StatusOptions{Resources: true})

	if !strings.Contains(out, "\ncontainers\n  Fake::Cluster  ocel-containers-production\n") {
		t.Errorf("output = %q, want the containers part's cluster under its name", out)
	}
}

func TestBootstrapStatusReadsBothTiersWhenNoneIsNamed(t *testing.T) {
	project, invocation := statusProject(t)

	out := runStatus(t, invocation, project.Root, nil, StatusOptions{})

	described := clitest.RequestsTo[*contractv1.DescribeBootstrapRequest](t, project.Requests, contractv1connect.ProviderServiceDescribeBootstrapProcedure)
	if len(described) != 2 || !described[0].GetWithParts() || described[1].GetTier() != environmentv1.Tier_TIER_PREVIEW {
		t.Errorf("DescribeBootstrap was asked %v, want both tiers with their parts", described)
	}
	if !strings.Contains(out, "\npreview · ") || !strings.Contains(out, "not bootstrapped") || !strings.Contains(out, "`ocel bootstrap preview`") {
		t.Errorf("output = %q, want the preview tier reported absent with the command that sets it up", out)
	}
}

func TestBootstrapStatusAsJSONIsOneDocumentWithEveryTiersParts(t *testing.T) {
	project, invocation := statusProject(t)
	invocation.Presentation = func(io.Writer) terminal.Presentation {
		return terminal.Resolve(terminal.Conditions{Format: terminal.FormatJSON})
	}

	report := clitest.DecodeResult(t, runStatus(t, invocation, project.Root, production, StatusOptions{}))

	tiers, _ := report["tiers"].([]any)
	if len(tiers) != 1 {
		t.Fatalf("json tiers = %v, want production alone", report["tiers"])
	}
	tier, _ := tiers[0].(map[string]any)
	parts, _ := tier["parts"].([]any)
	if tier["present"] != true || len(parts) != 3 {
		t.Fatalf("json tier = %v, want production present with three parts", tier)
	}
	base, _ := parts[0].(map[string]any)
	for field, want := range map[string]any{
		"name":             provider.BasePart,
		"addedAt":          "2026-09-02T10:00:00Z",
		"state":            "ready",
		"current":          true,
		"idleMonthlyFixed": "0.00",
		"currency":         "USD",
	} {
		if base[field] != want {
			t.Errorf("json base %s = %v, want %v", field, base[field], want)
		}
	}
}
