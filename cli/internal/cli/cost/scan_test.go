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

	"github.com/ocelhq/ocel/cli/internal/appbuilder"
	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/runui"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
)

const apiFunction = "project:" + clitest.FixtureSlug + "/environment:prod/app:api/fake_function:fn--api--api"

func scanFixture(t *testing.T) (string, cmddeps.Deps) {
	t.Helper()
	root, _ := clitest.SetUpDeployFixture(t)
	clitest.WriteUsageMonorepo(t, root)
	deps := clitest.NewDeps()
	clitest.StubBuild(&deps, []manifestbuilder.Function{
		{Route: "api", Runtime: manifestbuilder.Runtime{Name: "node"}, Handler: "src/server.js", ArtifactPath: "output/api", App: "api"},
	})
	return root, deps
}

func scan(t *testing.T, deps cmddeps.Deps, root string, opts Options) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), deps, root, opts, &stdout, &stderr); err != nil {
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
		root, deps := scanFixture(t)

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
		root, deps := scanFixture(t)

		out := scan(t, deps, root, Options{Profile: "heavy"})

		row := lineNaming(out, "fn--api--api")
		if !strings.Contains(row, "10.00") {
			t.Errorf("function row = %q, want the heavy profile's 10,000,000 requests priced at 10.00", row)
		}
	})

	t.Run("a usage file overrides the profile for the resource it names", func(t *testing.T) {
		root, deps := scanFixture(t)
		usage := filepath.Join(t.TempDir(), "usage.yaml")
		clitest.WriteFile(t, usage, apiFunction+":\n  monthly_requests: 5000000\n")

		out := scan(t, deps, root, Options{Usage: usage})

		row := lineNaming(out, "fn--api--api")
		if !strings.Contains(row, "5.00") {
			t.Errorf("function row = %q, want 5,000,000 requests from the file priced at 5.00", row)
		}
	})

	t.Run("it stands one function per app when nothing is built", func(t *testing.T) {
		root, deps := scanFixture(t)
		deps.CollectAppFunctions = func(string) ([]manifestbuilder.Function, error) {
			return nil, appbuilder.ErrNoBuildOutput
		}

		out := scan(t, deps, root, Options{})

		if !strings.Contains(out, "fake_function") {
			t.Errorf("stdout = %q, want the api app priced as one function without a build", out)
		}
		if !strings.Contains(out, "Assumption: nothing is built, so each serverless app is priced as one function") {
			t.Errorf("stdout = %q, want the one-function-per-app assumption said out loud", out)
		}
	})

	t.Run("it carries the unbuilt assumption in the JSON envelope and none when the build is read", func(t *testing.T) {
		root, deps := scanFixture(t)
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

		deps.CollectAppFunctions = func(string) ([]manifestbuilder.Function, error) {
			return nil, appbuilder.ErrNoBuildOutput
		}
		var unbuilt scanJSON
		if err := json.Unmarshal([]byte(scan(t, deps, root, Options{})), &unbuilt); err != nil {
			t.Fatal(err)
		}
		if len(unbuilt.Assumptions) != 1 || !strings.Contains(unbuilt.Assumptions[0], "one function") {
			t.Errorf("assumptions without a build = %v, want the one-function-per-app assumption", unbuilt.Assumptions)
		}
	})

	t.Run("it prices the preview environment when asked", func(t *testing.T) {
		root, deps := scanFixture(t)

		out := scan(t, deps, root, Options{Env: "preview"})

		if !strings.Contains(out, "preview") || strings.Contains(out, "prod") {
			t.Errorf("stdout = %q, want the preview environment scoped and prod absent", out)
		}
	})

	t.Run("it refuses an environment or profile it does not know", func(t *testing.T) {
		root, deps := scanFixture(t)

		var stdout, stderr bytes.Buffer
		if err := Run(context.Background(), deps, root, Options{Env: "staging"}, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "staging") {
			t.Errorf("Run(env=staging) err = %v, want a refusal naming what was typed", err)
		}
		if err := Run(context.Background(), deps, root, Options{Profile: "extreme"}, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "extreme") {
			t.Errorf("Run(profile=extreme) err = %v, want a refusal naming what was typed", err)
		}
	})

	t.Run("it writes the resource set and the estimate as JSON under --log-format json", func(t *testing.T) {
		root, deps := scanFixture(t)
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
