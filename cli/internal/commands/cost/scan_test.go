package cost

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/pricecache/pricecachetest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/workdir"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/pricing"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

const apiFunction = "environment:prod/app:api/fake_function:fn--api--api"

func TestAScanPricesANextContainerBuiltFromItsFunctionByNoImage(t *testing.T) {
	cfg := &project.Project{Slug: "shop", Dir: t.TempDir(), Apps: []project.App{
		{Name: "web", Framework: buildoutput.FrameworkNext, Compute: provider.ComputeContainer, Container: &project.Container{FromFunction: true}},
	}}
	dependencies := Dependencies{ReadFunctions: func(string) ([]build.Function, error) { return nil, build.ErrNoBuildOutput }}

	scanned, _, err := scannedOutput(dependencies, cfg)
	if err != nil {
		t.Fatalf("scannedOutput() = %v", err)
	}
	if len(scanned.Images) != 0 {
		t.Errorf("Images = %v, want none: the container's image is built from its function", scanned.Images)
	}
	if len(scanned.Functions) != 1 || scanned.Functions[0].App != "web" {
		t.Errorf("Functions = %+v, want the one function web is built from", scanned.Functions)
	}
}

func servePrices(t *testing.T, catalog pricing.Catalog) *pricecachetest.Console {
	t.Helper()
	prices := pricecachetest.Serve(t, catalog)
	t.Setenv(console.URLEnvVar, prices.URL)
	return prices
}

func scanFixture(t *testing.T) (string, *fake.Provider, Dependencies, *pricecachetest.Console) {
	t.Helper()
	project := clitest.SetUpProject(t)
	root, p := project.Root, project.Provider
	clitest.WriteUsageMonorepo(t, root)
	dependencies := newTestDependencies()
	dependencies.Prices.CachePath = filepath.Join(t.TempDir(), "answers.json")
	stubFunctions(&dependencies, []build.Function{
		{Route: "api", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/api", App: "api"},
	})
	return root, p, dependencies, servePrices(t, clitest.FakePriceCard(t))
}

func scan(t *testing.T, dependencies Dependencies, root string, opts Options) string {
	t.Helper()
	out, err := scanReading(t, dependencies, root, opts, "")
	if err != nil {
		t.Fatalf("Run err = %v; stdout=%s", err, out)
	}
	return out
}

func scanReading(t *testing.T, dependencies Dependencies, root string, opts Options, stdin string) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stderr)
	err := Run(context.Background(), dependencies, root, opts, strings.NewReader(stdin), &stdout)
	if err != nil {
		return stdout.String(), errors.Join(err, errors.New(stderr.String()))
	}
	return stdout.String(), nil
}

func asJSON(dependencies *Dependencies) {
	dependencies.Presentation = func(io.Writer) terminal.Presentation {
		return terminal.Resolve(terminal.Conditions{Format: terminal.FormatJSON})
	}
}

func decodeScan(t *testing.T, out string) *resultv1.CostScanResult {
	t.Helper()
	var result resultv1.CostScanResult
	clitest.DecodeResultInto(t, out, &result)
	return &result
}

func decodeDiff(t *testing.T, out string) *resultv1.CostDiffResult {
	t.Helper()
	var result resultv1.CostDiffResult
	clitest.DecodeResultInto(t, out, &result)
	return &result
}

func TestCostScanPricesEveryResourceUnderTheProfileAskedFor(t *testing.T) {
	t.Run("it tables every resource under its scope with the fixed and moderate cost beside it", func(t *testing.T) {
		root, _, dependencies, _ := scanFixture(t)

		out := scan(t, dependencies, root, Options{})

		for _, want := range []string{
			"prod",
			"api",
			"main",
			"14.60",
			"1.00",
			"moderate",
			"light",
			"0.10",
			"heavy",
			"10.00",
			"15.60",
			"0 unsupported",
			"0 without a price",
			"Prices: fake",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout lacks %q:\n%s", want, out)
			}
		}
	})

	t.Run("it prices the rows under the profile asked for", func(t *testing.T) {
		root, _, dependencies, _ := scanFixture(t)

		out := scan(t, dependencies, root, Options{Profile: "heavy"})

		row := lineNaming(out, "fn--api--api")
		if !strings.Contains(row, "10.00") {
			t.Errorf("function row = %q, want the heavy profile's 10,000,000 requests priced at 10.00", row)
		}
	})

	t.Run("a usage file overrides the profile for the resource it names", func(t *testing.T) {
		root, _, dependencies, _ := scanFixture(t)
		usage := filepath.Join(t.TempDir(), "usage.yaml")
		clitest.WriteFile(t, usage, apiFunction+":\n  monthly_requests: 5000000\n")

		out := scan(t, dependencies, root, Options{Usage: usage})

		row := lineNaming(out, "fn--api--api")
		if !strings.Contains(row, "5.00") {
			t.Errorf("function row = %q, want 5,000,000 requests from the file priced at 5.00", row)
		}
	})

	t.Run("it prices one function per app when nothing is built", func(t *testing.T) {
		root, _, dependencies, _ := scanFixture(t)
		dependencies.ReadFunctions = func(string) ([]build.Function, error) {
			return nil, build.ErrNoBuildOutput
		}

		out := scan(t, dependencies, root, Options{})

		if !strings.Contains(out, "fake_function") {
			t.Errorf("stdout = %q, want the api app priced as one function without a build", out)
		}
		if !strings.Contains(out, "Assumption: nothing is built, so each serverless app is priced as one function") {
			t.Errorf("stdout = %q, want the one-function-per-app assumption said out loud", out)
		}
	})

	t.Run("it includes the unbuilt assumption in the JSON envelope and none when the build is read", func(t *testing.T) {
		root, _, dependencies, _ := scanFixture(t)
		asJSON(&dependencies)

		built := decodeScan(t, scan(t, dependencies, root, Options{}))
		if len(built.GetAssumptions()) != 0 {
			t.Errorf("assumptions with a build = %v, want none", built.GetAssumptions())
		}

		dependencies.ReadFunctions = func(string) ([]build.Function, error) {
			return nil, build.ErrNoBuildOutput
		}
		unbuilt := decodeScan(t, scan(t, dependencies, root, Options{}))
		if len(unbuilt.GetAssumptions()) != 1 || !strings.Contains(unbuilt.GetAssumptions()[0], "one function") {
			t.Errorf("assumptions without a build = %v, want the one-function-per-app assumption", unbuilt.GetAssumptions())
		}
	})

	t.Run("an app naming no compute is priced on the compute its provider runs first", func(t *testing.T) {
		root, p, dependencies, _ := scanFixture(t)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "`+clitest.FixtureSlug+`",
  provider: { fake: {} },
  apps: [{ name: "api", path: "apps/api" }],
};
`)
		clitest.WriteFile(t, filepath.Join(root, "apps", "api", "package.json"), `{}`)
		p.WithFacts(func(facts *provider.Facts) { facts.Computes = []provider.Compute{provider.ComputeContainer} })
		dependencies.ReadFunctions = func(string) ([]build.Function, error) {
			return nil, build.ErrNoBuildOutput
		}

		out := scan(t, dependencies, root, Options{})

		if !strings.Contains(out, "fake_container") || strings.Contains(out, "fake_function") {
			t.Errorf("stdout = %q, want the api app priced as the container its provider runs, not as a serverless function", out)
		}
	})

	t.Run("it prices the preview environment when asked", func(t *testing.T) {
		root, _, dependencies, _ := scanFixture(t)

		out := scan(t, dependencies, root, Options{Env: "preview"})

		if !strings.Contains(out, "preview") || strings.Contains(out, "prod") {
			t.Errorf("stdout = %q, want the preview environment scoped and prod absent", out)
		}
	})

	t.Run("it refuses an environment or profile it does not know", func(t *testing.T) {
		root, _, dependencies, _ := scanFixture(t)

		if _, err := scanReading(t, dependencies, root, Options{Env: "staging"}, ""); err == nil || !strings.Contains(err.Error(), "staging") {
			t.Errorf("Run(env=staging) err = %v, want a refusal naming what was typed", err)
		}
		if _, err := scanReading(t, dependencies, root, Options{Profile: "extreme"}, ""); err == nil || !strings.Contains(err.Error(), "extreme") {
			t.Errorf("Run(profile=extreme) err = %v, want a refusal naming what was typed", err)
		}
	})

	t.Run("it writes the resource set and the estimate as JSON under --json", func(t *testing.T) {
		root, _, dependencies, _ := scanFixture(t)
		asJSON(&dependencies)

		got := decodeScan(t, scan(t, dependencies, root, Options{}))

		set, estimate := got.GetResources(), got.GetEstimate()
		if set.GetSource() != "ocel" || len(set.GetResources()) != 2 {
			t.Errorf("resources = %v, want the ocel source with the function and the postgres", set)
		}
		if estimate.GetProfile() != costv1.Profile_PROFILE_MODERATE || estimate.GetMonthlyFixed() != "14.60" || estimate.GetMonthlyUsage() != "1.00" {
			t.Errorf("estimate = profile %q fixed %q usage %q, want moderate 14.60 1.00", estimate.GetProfile(), estimate.GetMonthlyFixed(), estimate.GetMonthlyUsage())
		}
		if catalogs := estimate.GetCatalogs(); len(catalogs) == 0 || catalogs[0].GetVersion() != clitest.FakePriceCatalogVersion {
			t.Errorf("catalogs = %v, want the catalog version that answered", catalogs)
		}
	})
}

func TestAScanAsksTheConsoleOnceForEveryPriceOfEveryProfile(t *testing.T) {
	root, _, dependencies, prices := scanFixture(t)

	scan(t, dependencies, root, Options{})

	if calls := prices.Calls(); len(calls) != 1 {
		t.Errorf("the console was asked %d times, want one lookup for the three profiles", len(calls))
	}
	if auth := prices.Authorization(); len(auth) != 1 || auth[0] != "" {
		t.Errorf("Authorization = %q, want an anonymous lookup from a logged-out CLI", auth)
	}
}

func lineNaming(out, name string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, name) {
			return line
		}
	}
	return ""
}

func TestAScanAgainstAProviderThatTakesNoInventorySaysSoBeforeScanning(t *testing.T) {
	root, p, dependencies, _ := scanFixture(t)
	p.WithHooks(func(hooks *provider.Hooks) { hooks.Inventory = nil })
	scanned := false
	collect := dependencies.CollectDeclarations
	dependencies.CollectDeclarations = func(ctx context.Context, cfg *project.Project, declarations *variables.Declarations, stdout, stderr io.Writer) ([]declaration.Resource, error) {
		scanned = true
		return collect(ctx, cfg, declarations, stdout, stderr)
	}

	_, err := scanReading(t, dependencies, root, Options{}, "")

	if err == nil || !strings.Contains(err.Error(), "fake takes no inventory; pass --from a plan or state file") {
		t.Fatalf("Run err = %v, want it to say the provider takes no inventory", err)
	}
	if scanned {
		t.Error("the scan read the project before learning that the provider takes no inventory")
	}
}

func TestAScanStartsTheProviderInTheCheckPhaseOfItsRunAndPrintsItsEstimateAloneOnStdout(t *testing.T) {
	root, _, dependencies, _ := scanFixture(t)
	asJSON(&dependencies)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stderr)
	if err := Run(context.Background(), dependencies, root, Options{}, strings.NewReader(""), &stdout); err != nil {
		t.Fatalf("Run err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	var phases []progressv1.Phase
	var result *streamv1.RunSummary
	for _, line := range strings.Split(strings.TrimSpace(stderr.String()), "\n") {
		ev := &streamv1.RunEvent{}
		if err := protojson.Unmarshal([]byte(line), ev); err != nil {
			t.Fatalf("stream line %q is not a protojson RunEvent: %v", line, err)
		}
		if ev.GetOperation().GetStarted() != nil && len(ev.GetOperation().GetStarted().GetParentSpanId()) == 0 {
			phases = append(phases, ev.GetOperation().GetPhase())
		}
		if ev.GetSummary() != nil {
			result = ev.GetSummary()
		}
	}
	if len(phases) == 0 || phases[0] != progressv1.Phase_PHASE_CHECK {
		t.Errorf("phases = %v, want the run to open with the check phase that starts the provider", phases)
	}
	if !result.GetSuccess() {
		t.Errorf("result = %v, want the scan's run to succeed", result)
	}
	decodeScan(t, stdout.String())
}

func TestAScanReadsTheBuildOutputUnderTheBuildOutputLock(t *testing.T) {
	root, _, dependencies, _ := scanFixture(t)
	read := dependencies.ReadFunctions
	locked := false
	dependencies.ReadFunctions = func(projectDir string) ([]build.Function, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		release, err := workdir.LockBuildOutput(ctx, projectDir, func() { locked = true })
		if err == nil {
			release()
		}
		return read(projectDir)
	}

	scan(t, dependencies, root, Options{})

	if !locked {
		t.Error("the build output was read without the build-output lock held, so a concurrent build can remove it mid-scan")
	}
}

func fakeResource(id, typ string) *costv1.Resource {
	return &costv1.Resource{Id: id, Scope: "stack:dev", Vendor: clitest.FakePriceVendor, Type: typ, Name: id}
}

func resourceSet(source string, resources ...*costv1.Resource) *costv1.ResourceSet {
	return &costv1.ResourceSet{Source: source, Scopes: []*costv1.Scope{{Id: "stack:dev", Kind: "stack", Name: "dev"}}, Resources: resources}
}

func writeScanDocument(t *testing.T, set *costv1.ResourceSet) string {
	t.Helper()
	var document bytes.Buffer
	if err := terminal.WriteResultJSON(&document, &resultv1.CostScanResult{Resources: set}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "scan.json")
	clitest.WriteFile(t, path, document.String())
	return path
}

func standaloneDependencies(t *testing.T) Dependencies {
	t.Helper()
	dependencies := newTestDependencies()
	dependencies.Prices.CachePath = filepath.Join(t.TempDir(), "answers.json")
	return dependencies
}

func TestAScanFromADocumentNeedsNoProject(t *testing.T) {
	prices := servePrices(t, clitest.FakePriceCard(t))
	dependencies := standaloneDependencies(t)
	asJSON(&dependencies)
	from := writeScanDocument(t, resourceSet("pulumi", fakeResource("urn:db", "fake_postgres"), fakeResource("urn:fn", "fake_function")))

	out := scan(t, dependencies, t.TempDir(), Options{From: from})

	got := decodeScan(t, out)
	if got.GetResources().GetSource() != "pulumi" || got.GetEstimate().GetMonthlyFixed() != "14.60" || got.GetEstimate().GetMonthlyUsage() != "1.00" {
		t.Errorf("result = %v, want the document's pulumi set priced at 14.60 fixed and 1.00 usage", got)
	}
	if len(prices.Calls()) != 1 {
		t.Errorf("calls = %d, want one lookup", len(prices.Calls()))
	}
}

func TestAScanReadsItsDocumentFromStdin(t *testing.T) {
	servePrices(t, clitest.FakePriceCard(t))
	dependencies := standaloneDependencies(t)
	raw, err := os.ReadFile(writeScanDocument(t, resourceSet("terraform", fakeResource("fake_postgres.main", "fake_postgres"))))
	if err != nil {
		t.Fatal(err)
	}

	out, err := scanReading(t, dependencies, t.TempDir(), Options{From: "-"}, string(raw))

	if err != nil || !strings.Contains(out, "stdin") || !strings.Contains(lineNaming(out, "fake_postgres.main"), "14.60") {
		t.Errorf("Run() = %v\n%s\nwant the set read from stdin priced", err, out)
	}
}

func TestAScanSaysWhatItsDocumentLeavesOut(t *testing.T) {
	servePrices(t, clitest.FakePriceCard(t))
	dependencies := standaloneDependencies(t)
	asJSON(&dependencies)

	out, err := scanReading(t, dependencies, t.TempDir(), Options{From: "-"}, "[]")
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}

	if got := decodeScan(t, out).GetAssumptions(); len(got) != 1 || !strings.Contains(got[0], "sst diff") {
		t.Errorf("assumptions = %q, want the document's own assumption that an sst diff lists changes alone", got)
	}
}

func TestAScanRefusesFlagsThatContradictEachOther(t *testing.T) {
	servePrices(t, clitest.FakePriceCard(t))
	from := writeScanDocument(t, resourceSet("terraform", fakeResource("fake_postgres.main", "fake_postgres")))
	tests := []struct {
		name string
		opts Options
		want string
	}{
		{"an environment with a document", Options{From: from, Env: "preview"}, "--env"},
		{"a diff with a current set", Options{From: from, Diff: true, Against: from}, "--against"},
		{"a diff without a document", Options{Diff: true}, "pass --from"},
		{"both documents from stdin", Options{From: "-", Against: "-"}, "stdin"},
		{"a diff of a document with no prior state", Options{From: from, Diff: true}, "carries no prior state"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := scanReading(t, standaloneDependencies(t), t.TempDir(), tt.opts, "")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Run() err = %v, want a refusal naming %q", err, tt.want)
			}
		})
	}
}

func TestADiffPricesBothSetsInOneLookupAndShowsWhatChanges(t *testing.T) {
	prices := servePrices(t, clitest.FakePriceCard(t))
	current := writeScanDocument(t, resourceSet("terraform",
		fakeResource("fake_postgres.main", "fake_postgres"),
		fakeResource("fake_bucket.old", "fake_bucket"),
		fakeResource("fake_function.api", "fake_function"),
	))
	proposed := writeScanDocument(t, resourceSet("terraform",
		fakeResource("fake_postgres.main", "fake_postgres"),
		fakeResource("fake_function.api", "fake_function"),
		fakeResource("fake_container.web", "fake_container"),
	))
	usage := filepath.Join(t.TempDir(), "usage.yaml")
	clitest.WriteFile(t, usage, "fake_function.api:\n  monthly_requests: 3000000\n")

	t.Run("it reports added, removed and changed resources with signed deltas", func(t *testing.T) {
		dependencies := standaloneDependencies(t)
		asJSON(&dependencies)

		got := decodeDiff(t, scan(t, dependencies, t.TempDir(), Options{From: proposed, Against: current}))

		if calls := len(prices.Calls()); calls != 1 {
			t.Errorf("the console was asked %d times, want one lookup for both sets", calls)
		}
		changes := map[string]*costv1.ResourceChange{}
		for _, change := range got.GetDiff().GetResources() {
			changes[change.GetResource()] = change
		}
		if len(changes) != 2 {
			t.Errorf("changes = %v, want the added container and the removed bucket only", got.GetDiff().GetResources())
		}
		if added := changes["fake_container.web"]; added.GetChange() != costv1.ResourceChange_CHANGE_ADDED || added.GetMonthlyFixedDelta() != "7.30" {
			t.Errorf("container = %v, want added at +7.30 fixed", added)
		}
		if removed := changes["fake_bucket.old"]; removed.GetChange() != costv1.ResourceChange_CHANGE_REMOVED || removed.GetMonthlyUsageDelta() != "-0.10" {
			t.Errorf("bucket = %v, want removed at -0.10 usage", removed)
		}
		if got.GetDiff().GetMonthlyFixedDelta() != "7.30" || got.GetDiff().GetMonthlyUsageDelta() != "-0.10" {
			t.Errorf("deltas = %q %q, want 7.30 and -0.10", got.GetDiff().GetMonthlyFixedDelta(), got.GetDiff().GetMonthlyUsageDelta())
		}
		if got.GetCurrent().GetSource() != "terraform" || got.GetProposed().GetSource() != "terraform" {
			t.Errorf("sets = %v / %v, want both terraform sets", got.GetCurrent(), got.GetProposed())
		}
	})

	t.Run("it renders signed deltas and both totals", func(t *testing.T) {
		out := scan(t, standaloneDependencies(t), t.TempDir(), Options{From: proposed, Against: current})

		for _, want := range []string{"fake_container.web", "added", "+7.30", "fake_bucket.old", "removed", "-0.10", "Current 15.70 → proposed 22.90"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout lacks %q:\n%s", want, out)
			}
		}
	})

	t.Run("a usage file applies to the resource in both sets", func(t *testing.T) {
		dependencies := standaloneDependencies(t)
		asJSON(&dependencies)

		got := decodeDiff(t, scan(t, dependencies, t.TempDir(), Options{From: proposed, Against: current, Usage: usage}))

		if got.GetDiff().GetCurrent().GetMonthlyUsage() != "3.10" || got.GetDiff().GetProposed().GetMonthlyUsage() != "3.00" {
			t.Errorf("usage = %q → %q, want the 3,000,000 requests priced on both sides", got.GetDiff().GetCurrent().GetMonthlyUsage(), got.GetDiff().GetProposed().GetMonthlyUsage())
		}
	})

	t.Run("it refuses a usage file naming a resource in neither set", func(t *testing.T) {
		stray := filepath.Join(t.TempDir(), "usage.yaml")
		clitest.WriteFile(t, stray, "fake_function.gone:\n  monthly_requests: 1\n")

		_, err := scanReading(t, standaloneDependencies(t), t.TempDir(), Options{From: proposed, Against: current, Usage: stray}, "")

		if err == nil || !strings.Contains(err.Error(), "fake_function.gone") {
			t.Errorf("Run() err = %v, want a refusal naming the stray resource", err)
		}
	})

	t.Run("it refuses sets from different tools", func(t *testing.T) {
		pulumi := writeScanDocument(t, resourceSet("pulumi", fakeResource("urn:db", "fake_postgres")))

		_, err := scanReading(t, standaloneDependencies(t), t.TempDir(), Options{From: pulumi, Against: current}, "")

		if err == nil || !strings.Contains(err.Error(), "terraform") || !strings.Contains(err.Error(), "pulumi") {
			t.Errorf("Run() err = %v, want a refusal naming both tools", err)
		}
	})
}

type unclearCatalog struct{ card pricing.Catalog }

func (c unclearCatalog) Lookup(ctx context.Context, queries []pricing.Query) ([]pricing.Answer, error) {
	answers, err := c.card.Lookup(ctx, queries)
	for i, query := range queries {
		switch query.Service {
		case "Requests":
			answers[i] = pricing.Answer{Kind: pricing.AnswerAmbiguous, Candidates: []string{"SKU-A", "SKU-B"}, Matches: 2}
		case "Storage":
			answers[i] = pricing.Answer{Kind: pricing.AnswerMissing, Reason: "no product matches"}
		}
	}
	return answers, err
}

func TestAPriceTheCatalogCannotGiveIsShownAndNeverCountedAsZero(t *testing.T) {
	servePrices(t, unclearCatalog{clitest.FakePriceCard(t)})
	from := writeScanDocument(t, resourceSet("terraform",
		fakeResource("fake_postgres.main", "fake_postgres"),
		fakeResource("fake_function.api", "fake_function"),
		fakeResource("fake_bucket.files", "fake_bucket"),
	))

	out := scan(t, standaloneDependencies(t), t.TempDir(), Options{From: from})

	for _, want := range []string{"! Requests: ambiguous price", "SKU-A", "! Storage: no price", "Unpriced: 2 components have no price, so these totals leave them out\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if row := lineNaming(out, "fake_function.api"); strings.Contains(row, "0.00") {
		t.Errorf("function row = %q, want no price shown rather than 0.00", row)
	}
}

func TestAnOfflineScanPricesFromTheCacheAlone(t *testing.T) {
	prices := servePrices(t, clitest.FakePriceCard(t))
	dependencies := standaloneDependencies(t)
	cached := writeScanDocument(t, resourceSet("terraform", fakeResource("fake_postgres.main", "fake_postgres")))
	scan(t, dependencies, t.TempDir(), Options{From: cached})
	asked := len(prices.Calls())

	t.Run("it serves cached prices without asking and says how old they are", func(t *testing.T) {
		prices.RefuseNext(connect.NewError(connect.CodeUnavailable, errors.New("down")))

		out := scan(t, dependencies, t.TempDir(), Options{From: cached, Offline: true})

		if len(prices.Calls()) != asked {
			t.Errorf("the console was asked %d more times, want none offline", len(prices.Calls())-asked)
		}
		if !strings.Contains(lineNaming(out, "fake_postgres.main"), "14.60") || !strings.Contains(out, "Note: prices cached up to") {
			t.Errorf("stdout = %s, want the cached price and its age", out)
		}
	})

	t.Run("it shows a price nothing cached as unavailable", func(t *testing.T) {
		uncached := writeScanDocument(t, resourceSet("terraform", fakeResource("fake_container.web", "fake_container")))

		out := scan(t, dependencies, t.TempDir(), Options{From: uncached, Offline: true})

		if got := strings.TrimSpace(lineNaming(out, "! Container:")); got != "! Container: no cached price, --offline asks the console for none" {
			t.Errorf("container line = %q, want the uncached price shown as unavailable, said once", got)
		}
		if got := lineNaming(out, "Unpriced:"); got != "Unpriced: 1 component has no price, so these totals leave it out" {
			t.Errorf("unpriced line = %q, want one component counted in a sentence", got)
		}
	})
}

func TestAScanWhoseConsoleDoesNotAnswerNamesItAndTheOfflineFlag(t *testing.T) {
	prices := servePrices(t, clitest.FakePriceCard(t))
	prices.RefuseNext(connect.NewError(connect.CodeFailedPrecondition, errors.New("closed")))
	from := writeScanDocument(t, resourceSet("terraform", fakeResource("fake_postgres.main", "fake_postgres")))

	_, err := scanReading(t, standaloneDependencies(t), t.TempDir(), Options{From: from}, "")

	if err == nil || !strings.Contains(err.Error(), prices.URL) || !strings.Contains(err.Error(), "--offline") {
		t.Errorf("Run() err = %v, want a refusal naming %s and --offline", err, prices.URL)
	}
}
