package cost

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/runui"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

const apiFunction = "environment:prod/app:api/fake_function:fn--api--api"

func scanFixture(t *testing.T) (string, *fake.Provider, cmddeps.Deps) {
	t.Helper()
	project := clitest.SetUpProject(t)
	root, p := project.Root, project.Provider
	clitest.WriteUsageMonorepo(t, root)
	deps := clitest.NewDeps()
	clitest.StubBuild(&deps, []manifestbuilder.Function{
		{Route: "api", Framework: manifestbuilder.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/api", App: "api"},
	})
	return root, p, deps
}

func scan(t *testing.T, deps cmddeps.Deps, root string, opts Options) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(deps, &stderr)
	if err := Run(context.Background(), deps, root, opts, &stdout); err != nil {
		t.Fatalf("Run err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

type scanJSON struct {
	Resources   json.RawMessage `json:"resources"`
	Estimate    json.RawMessage `json:"estimate"`
	Assumptions []string        `json:"assumptions"`
}

func TestScan(t *testing.T) {
	t.Run("it tables every resource under its scope with the fixed and moderate cost beside it", func(t *testing.T) {
		root, _, deps := scanFixture(t)

		out := scan(t, deps, root, Options{})

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
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout lacks %q:\n%s", want, out)
			}
		}
	})

	t.Run("it prices the rows under the profile asked for", func(t *testing.T) {
		root, _, deps := scanFixture(t)

		out := scan(t, deps, root, Options{Profile: "heavy"})

		row := lineNaming(out, "fn--api--api")
		if !strings.Contains(row, "10.00") {
			t.Errorf("function row = %q, want the heavy profile's 10,000,000 requests priced at 10.00", row)
		}
	})

	t.Run("a usage file overrides the profile for the resource it names", func(t *testing.T) {
		root, _, deps := scanFixture(t)
		usage := filepath.Join(t.TempDir(), "usage.yaml")
		clitest.WriteFile(t, usage, apiFunction+":\n  monthly_requests: 5000000\n")

		out := scan(t, deps, root, Options{Usage: usage})

		row := lineNaming(out, "fn--api--api")
		if !strings.Contains(row, "5.00") {
			t.Errorf("function row = %q, want 5,000,000 requests from the file priced at 5.00", row)
		}
	})

	t.Run("it prices one function per app when nothing is built", func(t *testing.T) {
		root, _, deps := scanFixture(t)
		deps.ReadFunctions = func(string) ([]manifestbuilder.Function, error) {
			return nil, build.ErrNoBuildOutput
		}

		out := scan(t, deps, root, Options{})

		if !strings.Contains(out, "fake_function") {
			t.Errorf("stdout = %q, want the api app priced as one function without a build", out)
		}
		if !strings.Contains(out, "Assumption: nothing is built, so each serverless app is priced as one function") {
			t.Errorf("stdout = %q, want the one-function-per-app assumption said out loud", out)
		}
	})

	t.Run("it includes the unbuilt assumption in the JSON envelope and none when the build is read", func(t *testing.T) {
		root, _, deps := scanFixture(t)
		deps.Presentation = func(io.Writer) runui.Presentation {
			return runui.Resolve(runui.Origin{LogFormat: runui.FormatJSON})
		}

		var built scanJSON
		if err := json.Unmarshal([]byte(scan(t, deps, root, Options{})), &built); err != nil {
			t.Fatal(err)
		}
		if len(built.Assumptions) != 0 {
			t.Errorf("assumptions with a build = %v, want none", built.Assumptions)
		}

		deps.ReadFunctions = func(string) ([]manifestbuilder.Function, error) {
			return nil, build.ErrNoBuildOutput
		}
		var unbuilt scanJSON
		if err := json.Unmarshal([]byte(scan(t, deps, root, Options{})), &unbuilt); err != nil {
			t.Fatal(err)
		}
		if len(unbuilt.Assumptions) != 1 || !strings.Contains(unbuilt.Assumptions[0], "one function") {
			t.Errorf("assumptions without a build = %v, want the one-function-per-app assumption", unbuilt.Assumptions)
		}
	})

	t.Run("an app naming no compute is priced on the compute its provider runs first", func(t *testing.T) {
		root, p, deps := scanFixture(t)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "`+clitest.FixtureSlug+`",
  provider: { fake: {} },
  apps: [{ name: "api", path: "apps/api" }],
};
`)
		clitest.WriteFile(t, filepath.Join(root, "apps", "api", "package.json"), `{}`)
		p.WithFacts(func(facts *provider.Facts) { facts.Computes = []provider.Compute{provider.ComputeContainer} })
		deps.ReadFunctions = func(string) ([]manifestbuilder.Function, error) {
			return nil, build.ErrNoBuildOutput
		}

		out := scan(t, deps, root, Options{})

		if !strings.Contains(out, "fake_container") || strings.Contains(out, "fake_function") {
			t.Errorf("stdout = %q, want the api app priced as the container its provider runs, not as a serverless function", out)
		}
	})

	t.Run("it prices the preview environment when asked", func(t *testing.T) {
		root, _, deps := scanFixture(t)

		out := scan(t, deps, root, Options{Env: "preview"})

		if !strings.Contains(out, "preview") || strings.Contains(out, "prod") {
			t.Errorf("stdout = %q, want the preview environment scoped and prod absent", out)
		}
	})

	t.Run("it refuses an environment or profile it does not know", func(t *testing.T) {
		root, _, deps := scanFixture(t)

		var stdout bytes.Buffer
		if err := Run(context.Background(), deps, root, Options{Env: "staging"}, &stdout); err == nil || !strings.Contains(err.Error(), "staging") {
			t.Errorf("Run(env=staging) err = %v, want a refusal naming what was typed", err)
		}
		if err := Run(context.Background(), deps, root, Options{Profile: "extreme"}, &stdout); err == nil || !strings.Contains(err.Error(), "extreme") {
			t.Errorf("Run(profile=extreme) err = %v, want a refusal naming what was typed", err)
		}
	})

	t.Run("it writes the resource set and the estimate as JSON under --log-format json", func(t *testing.T) {
		root, _, deps := scanFixture(t)
		deps.Presentation = func(io.Writer) runui.Presentation {
			return runui.Resolve(runui.Origin{LogFormat: runui.FormatJSON})
		}

		out := scan(t, deps, root, Options{})

		var got scanJSON
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("stdout is not one JSON object: %v\n%s", err, out)
		}
		var set costv1.ResourceSet
		if err := protojson.Unmarshal(got.Resources, &set); err != nil {
			t.Fatalf("resources is not a ResourceSet: %v", err)
		}
		var estimate costv1.Estimate
		if err := protojson.Unmarshal(got.Estimate, &estimate); err != nil {
			t.Fatalf("estimate is not an Estimate: %v", err)
		}
		if set.GetSource() != "ocel" || len(set.GetResources()) != 2 {
			t.Errorf("resources = %v, want the ocel source with the function and the postgres", &set)
		}
		if estimate.GetProfile() != costv1.Profile_PROFILE_MODERATE || estimate.GetMonthlyFixed() != "14.60" || estimate.GetMonthlyUsage() != "1.00" {
			t.Errorf("estimate = profile %q fixed %q usage %q, want moderate 14.60 1.00", estimate.GetProfile(), estimate.GetMonthlyFixed(), estimate.GetMonthlyUsage())
		}
	})
}

func lineNaming(out, name string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, name) {
			return line
		}
	}
	return ""
}

func TestAScanStartsTheProviderInTheCheckPhaseOfItsRunAndPrintsItsEstimateAloneOnStdout(t *testing.T) {
	root, _, deps := scanFixture(t)
	deps.Presentation = func(io.Writer) runui.Presentation {
		return runui.Resolve(runui.Origin{LogFormat: runui.FormatJSON})
	}

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(deps, &stderr)
	if err := Run(context.Background(), deps, root, Options{}, &stdout); err != nil {
		t.Fatalf("Run err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	var phases []progressv1.Phase
	var result *streamv1.RunSummary
	for _, line := range strings.Split(strings.TrimSpace(stderr.String()), "\n") {
		ev := &streamv1.RunEvent{}
		if err := protojson.Unmarshal([]byte(line), ev); err != nil {
			t.Fatalf("stream line %q is not a protojson RunEvent: %v", line, err)
		}
		if ev.GetStarted() != nil && len(ev.GetStarted().GetParentSpanId()) == 0 {
			phases = append(phases, ev.GetPhase())
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
	var scanned scanJSON
	if err := json.Unmarshal(stdout.Bytes(), &scanned); err != nil {
		t.Errorf("stdout = %q, want the estimate as one JSON document: %v", stdout.String(), err)
	}
}
