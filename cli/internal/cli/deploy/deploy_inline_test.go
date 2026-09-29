package deploy

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

const inlineURL = "postgres://app:s3cret-pw@ep-cool.neon.tech/main?sslmode=require"

const inlineByURL = `postgres: { main: { url: { $env: "MAIN_DATABASE_URL" } } }`

type inlineRun struct {
	root    string
	journal string
}

func setUpInline(t *testing.T, bindings string) inlineRun {
	t.Helper()
	root, _ := clitest.SetUpDeployFixture(t)
	writeBoundMonorepo(t, root, bindings)
	t.Setenv(clitest.FakeVarsStoreEnvVar, filepath.Join(t.TempDir(), "vars.json"))
	t.Setenv(clitest.FakeBindingsStoreEnvVar, filepath.Join(t.TempDir(), "bindings.json"))
	journal := filepath.Join(t.TempDir(), "deploy.json")
	t.Setenv(clitest.FakeDeployJournalEnvVar, journal)
	return inlineRun{root: root, journal: journal}
}

func seedProduction(t *testing.T, key, value string) {
	t.Helper()
	store, err := clitest.LoadFakeStore()
	if err != nil {
		t.Fatal(err)
	}
	c := &envvarsv1.Coordinate{Slug: clitest.FixtureSlug, Key: key}
	store[clitest.FakeCoordinateID(environmentv1.Tier_TIER_PRODUCTION, c)] = &clitest.FakeCell{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: clitest.FakeCoordinate{Slug: clitest.FixtureSlug, Key: key},
		Versions:   []clitest.FakeCellData{{Value: value, Ts: 1_700_000_000}},
	}
	if err := clitest.SaveFakeStore(store); err != nil {
		t.Fatal(err)
	}
}

func (r inlineRun) deploy(t *testing.T, opts deployOptions) (string, error) {
	t.Helper()
	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)
	clitest.StubBuild(&deps, []build.Function{
		{Route: "api", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/api", App: "api"},
	})
	opts.yes = true
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(deps, &stdout)
	err := runDeploy(context.Background(), deps, r.root, opts, &stdout, &stderr, strings.NewReader(""))
	return stdout.String() + stderr.String(), err
}

func sentRequest(t *testing.T, journal string) *contractv1.DeployRequest {
	t.Helper()
	raw, err := os.ReadFile(journal)
	if err != nil {
		t.Fatalf("read the deploy request: %v", err)
	}
	req := &contractv1.DeployRequest{}
	if err := protojson.Unmarshal(raw, req); err != nil {
		t.Fatalf("decode the deploy request: %v", err)
	}
	return req
}

func carriedNames(carried []*bindingsv1.Binding) []string {
	names := make([]string, 0, len(carried))
	for _, binding := range carried {
		names = append(names, binding.GetName()+" from "+binding.GetSource())
	}
	return names
}

func TestDeployBindsAnInlineRecord(t *testing.T) {
	t.Run("the request carries the record whole and the manifest names it, never its secret", func(t *testing.T) {
		run := setUpInline(t, inlineByURL)
		seedProduction(t, "MAIN_DATABASE_URL", inlineURL)

		out, err := run.deploy(t, deployOptions{})
		if err != nil {
			t.Fatalf("deploy: %v\n%s", err, out)
		}
		if !strings.Contains(out, "BINDING bound=db--main name=main record=ocel:postgres.main carried") {
			t.Errorf("output = %q, want main bound to the record the request carries", out)
		}
		req := sentRequest(t, run.journal)
		carried := req.GetInlineBindings()
		if len(carried) != 1 || carried[0].GetName() != "ocel:postgres.main" || carried[0].GetSource() != "ocel.config.ts" {
			t.Fatalf("inline bindings = %v, want the one record, sourced from the config", carriedNames(carried))
		}
		if carried[0].GetPostgres().GetUrl() != inlineURL {
			t.Error("the carried record lost the url the app connects with")
		}
		manifest, err := protojson.Marshal(req.GetManifest())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(manifest), "s3cret-pw") {
			t.Errorf("the manifest contains the database password: %s", manifest)
		}
		if strings.Contains(out, "s3cret-pw") {
			t.Errorf("the deploy printed the database password: %s", out)
		}
	})

	t.Run("a variable the binding reads and nobody set refuses the deploy with the command that sets it", func(t *testing.T) {
		run := setUpInline(t, inlineByURL)

		out, err := run.deploy(t, deployOptions{})
		if err == nil {
			t.Fatalf("deploy succeeded with MAIN_DATABASE_URL unset\n%s", out)
		}
		if said := err.Error() + out; !strings.Contains(said, "ocel env set MAIN_DATABASE_URL=<VALUE>") {
			t.Errorf("refusal = %q, want the command that sets it", said)
		}
		if _, statErr := os.Stat(run.journal); statErr == nil {
			t.Error("the deploy reached the provider without the record's value")
		}
	})

	t.Run("a dry run hands the provider the record to plan with", func(t *testing.T) {
		run := setUpInline(t, inlineByURL)
		seedProduction(t, "MAIN_DATABASE_URL", inlineURL)

		if out, err := run.deploy(t, deployOptions{dry: true}); err != nil {
			t.Fatalf("dry deploy: %v\n%s", err, out)
		}
		req := sentRequest(t, run.journal)
		if !req.GetDry() || len(req.GetInlineBindings()) != 1 {
			t.Errorf("request dry = %v with %d inline bindings, want the dry plan to carry the record", req.GetDry(), len(req.GetInlineBindings()))
		}
	})

	t.Run("dropping the binding leaves the request carrying no record", func(t *testing.T) {
		run := setUpInline(t, inlineByURL)
		seedProduction(t, "MAIN_DATABASE_URL", inlineURL)
		if out, err := run.deploy(t, deployOptions{}); err != nil {
			t.Fatalf("first deploy: %v\n%s", err, out)
		}

		writeBoundMonorepo(t, run.root, ``)
		if out, err := run.deploy(t, deployOptions{}); err != nil {
			t.Fatalf("second deploy: %v\n%s", err, out)
		}
		if carried := sentRequest(t, run.journal).GetInlineBindings(); len(carried) != 0 {
			t.Errorf("inline bindings = %v, want none: the provider prunes what the request no longer carries", carriedNames(carried))
		}
	})
}
