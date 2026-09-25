package deploy

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
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
	clitest.StubBuild(&deps, []manifestbuilder.Function{
		{Route: "api", Runtime: manifestbuilder.Runtime{Name: "node"}, Handler: "src/server.js", ArtifactPath: "output/api", App: "api"},
	})
	opts.yes = true
	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), deps, r.root, opts, &stdout, &stderr, strings.NewReader(""))
	return stdout.String() + stderr.String(), err
}

func records(t *testing.T) []clitest.FakeBindingRecord {
	t.Helper()
	held, err := clitest.FakeBindingRecords()
	if err != nil {
		t.Fatal(err)
	}
	return held
}

func TestDeployBindsAnInlineRecord(t *testing.T) {
	t.Run("the record is kept for the deploy and the manifest names it, never its secret", func(t *testing.T) {
		run := setUpInline(t, inlineByURL)
		seedProduction(t, "MAIN_DATABASE_URL", inlineURL)

		out, err := run.deploy(t, deployOptions{})
		if err != nil {
			t.Fatalf("deploy: %v\n%s", err, out)
		}
		if !strings.Contains(out, "BINDING bound=db--main name=main record=ocel:postgres.main owner=ocel-config") {
			t.Errorf("output = %q, want main bound to the record ocel keeps for it", out)
		}
		held := records(t)
		if len(held) != 1 || held[0].Name != "ocel:postgres.main" || held[0].Tier != environmentv1.Tier_TIER_PRODUCTION {
			t.Fatalf("records = %+v, want the one inline record in production", held)
		}
		if !strings.Contains(held[0].Wire, "s3cret-pw") || !strings.Contains(held[0].Wire, `"source":"ocel.config.ts"`) {
			t.Errorf("record = %s, want the url whole and the config named as its source", held[0].Wire)
		}
		sent, err := os.ReadFile(run.journal)
		if err != nil {
			t.Fatalf("read the deploy request: %v", err)
		}
		if strings.Contains(string(sent), "s3cret-pw") {
			t.Errorf("the deploy request carries the database password: %s", sent)
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
		if held := records(t); len(held) != 0 {
			t.Errorf("records = %+v, want nothing kept for a refused deploy", held)
		}
	})

	t.Run("a server of another major version is refused before anything is deployed", func(t *testing.T) {
		run := setUpInline(t, inlineByURL)
		seedProduction(t, "MAIN_DATABASE_URL", inlineURL)
		t.Setenv(clitest.FakePostgresVersionEnvVar, "150004")

		out, err := run.deploy(t, deployOptions{})
		if err == nil {
			t.Fatalf("deploy succeeded against postgres 15 for a resource declaring 17\n%s", out)
		}
		for _, want := range []string{"declares postgres 17", "serves postgres 15", "bindings.postgres.main"} {
			if !strings.Contains(err.Error()+out, want) {
				t.Errorf("refusal = %q, want it to name %s", err.Error()+out, want)
			}
		}
		if _, statErr := os.Stat(run.journal); statErr == nil {
			t.Error("the deploy reached the provider after the check refused it")
		}
		if held := records(t); len(held) != 0 {
			t.Errorf("records = %+v, want nothing kept", held)
		}
	})

	t.Run("an unreachable server is refused without the password", func(t *testing.T) {
		run := setUpInline(t, `postgres: { main: { host: "db.example.com", database: "main", username: "app", password: { $env: "MAIN_PASSWORD" } } }`)
		seedProduction(t, "MAIN_PASSWORD", "s3cret-pw")
		t.Setenv(clitest.FakePostgresUnreachableEnvVar, "connection refused")

		out, err := run.deploy(t, deployOptions{})
		if err == nil {
			t.Fatalf("deploy succeeded against an unreachable server\n%s", out)
		}
		if strings.Contains(err.Error()+out, "s3cret-pw") {
			t.Errorf("refusal = %q, repeats the password", err)
		}
		if !strings.Contains(err.Error()+out, "connection refused") {
			t.Errorf("refusal = %q, want the cause", err.Error()+out)
		}
	})

	t.Run("a dry run checks the record and keeps nothing", func(t *testing.T) {
		run := setUpInline(t, inlineByURL)
		seedProduction(t, "MAIN_DATABASE_URL", inlineURL)

		if out, err := run.deploy(t, deployOptions{dry: true}); err != nil {
			t.Fatalf("dry deploy: %v\n%s", err, out)
		}
		if held := records(t); len(held) != 0 {
			t.Errorf("records = %+v, want a dry run to write nothing", held)
		}
	})

	t.Run("dropping the binding removes the record on the next deploy", func(t *testing.T) {
		run := setUpInline(t, inlineByURL)
		seedProduction(t, "MAIN_DATABASE_URL", inlineURL)
		if out, err := run.deploy(t, deployOptions{}); err != nil {
			t.Fatalf("first deploy: %v\n%s", err, out)
		}

		writeBoundMonorepo(t, run.root, ``)
		if out, err := run.deploy(t, deployOptions{}); err != nil {
			t.Fatalf("second deploy: %v\n%s", err, out)
		}
		if held := records(t); len(held) != 0 {
			t.Errorf("records = %+v, want the record no binding keeps removed", held)
		}
	})
}
