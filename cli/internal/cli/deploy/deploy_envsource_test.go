package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

const infisicalProduction = `
export default {
  slug: "` + clitest.FixtureSlug + `",
  provider: { aws: {} },
  domains: { preview: "*.preview.acme.com" },
  envSource: {
    production: { infisical: { project: "p-1", environment: "prod", auth: { universal: { clientId: { $env: "INFISICAL_CLIENT_ID" }, clientSecret: { $env: "INFISICAL_CLIENT_SECRET" } } } } },
  },
};
`

const stripeDeclared = `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true,"description":"Stripe's secret key"}]`

func setUpInfisicalFixture(t *testing.T, config string, source clitest.FakeEnvSource) string {
	t.Helper()
	root := clitest.SetUpVariablesFixtureWith(t, stripeDeclared, clitest.EnvDeclareOnlyScript)
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), config)
	envSet(t, root, "INFISICAL_CLIENT_ID", "client-id", envOptions{})
	envSet(t, root, "INFISICAL_CLIENT_SECRET", "client-secret", envOptions{})
	useFakeEnvSource(t, source)
	return root
}

func useFakeEnvSource(t *testing.T, source clitest.FakeEnvSource) {
	t.Helper()
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(clitest.FakeEnvSourceEnvVar, string(raw))
}

var infisicalURLs = map[string]string{"": "https://infisical.example/p-1/prod"}

func TestADeployReadsItsTiersEnvSourceBeforeCheckingItsVariables(t *testing.T) {
	t.Run("a value the env source has passes the variables check", func(t *testing.T) {
		root := setUpInfisicalFixture(t, infisicalProduction, clitest.FakeEnvSource{
			Values: []clitest.FakeEnvSourceValue{{Key: "STRIPE_API_KEY", Value: "sk_live_from_infisical"}},
			URLs:   infisicalURLs,
		})

		var stdout, stderr bytes.Buffer
		deps := clitest.NewDeps()
		clitest.AttachTerminalSink(deps, &stdout)
		if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "Deployed") {
			t.Errorf("stdout = %q, want the deploy to complete on the env source's value", stdout.String())
		}
	})

	t.Run("a value the env source lacks refuses, naming where to set it", func(t *testing.T) {
		root := setUpInfisicalFixture(t, infisicalProduction, clitest.FakeEnvSource{URLs: infisicalURLs})

		var stdout, stderr bytes.Buffer
		deps := clitest.NewDeps()
		clitest.AttachTerminalSink(deps, &stdout)
		err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want the variables check to refuse")
		}
		out := stdout.String()
		for _, want := range []string{"STRIPE_API_KEY", "set it in infisical:p-1/prod", "https://infisical.example/p-1/prod"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want %q", out, want)
			}
		}
		if strings.Contains(out, "ocel env set STRIPE_API_KEY") {
			t.Errorf("stdout = %q, want no `ocel env set` offered for a value the env source owns", out)
		}
	})

	t.Run("what the env source has and nothing declares is a warning, not a refusal", func(t *testing.T) {
		root := setUpInfisicalFixture(t, infisicalProduction, clitest.FakeEnvSource{
			Values: []clitest.FakeEnvSourceValue{{Key: "STRIPE_API_KEY", Value: "sk"}, {Key: "OLD_TOKEN", Value: "old"}},
		})

		var stdout, stderr bytes.Buffer
		deps := clitest.NewDeps()
		clitest.AttachTerminalSink(deps, &stdout)
		if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s", err, stdout.String())
		}
		if out := stdout.String(); !strings.Contains(out, "OLD_TOKEN") || !strings.Contains(out, "nothing this project declares") {
			t.Errorf("stdout = %q, want OLD_TOKEN reported as undeclared", out)
		}
	})

	t.Run("an env source that cannot be read stops the deploy before anything is built", func(t *testing.T) {
		root := setUpInfisicalFixture(t, infisicalProduction, clitest.FakeEnvSource{ReadError: "Infisical answered 503"})
		deps := clitest.NewDeps()
		built := false
		stubAppBuildRecorder(&deps, &built)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil || built {
			t.Fatalf("runDeploy err = %v, built = %v, want an unreadable env source to stop the deploy first", err, built)
		}
		if out := stdout.String() + err.Error(); !strings.Contains(out, "503") {
			t.Errorf("output = %q, want the env source's failure named", out)
		}
	})

	t.Run("an unset credential stops the deploy with the command that sets it", func(t *testing.T) {
		root := clitest.SetUpVariablesFixtureWith(t, stripeDeclared, clitest.EnvDeclareOnlyScript)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), infisicalProduction)
		envSet(t, root, "INFISICAL_CLIENT_ID", "client-id", envOptions{})
		useFakeEnvSource(t, clitest.FakeEnvSource{})

		var stdout, stderr bytes.Buffer
		deps := clitest.NewDeps()
		clitest.AttachTerminalSink(deps, &stdout)
		err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		out := stdout.String()
		if err == nil || !strings.Contains(out, "1 variable is not ready") || !strings.Contains(out, "INFISICAL_CLIENT_SECRET  root  no value") {
			t.Fatalf("runDeploy err = %v; stdout=%s, want the variables check to refuse the unset credential alone", err, out)
		}
		if !strings.Contains(out, "ocel env set INFISICAL_CLIENT_SECRET=<VALUE>") {
			t.Errorf("stdout = %q, want the credential's command named, not the env source", out)
		}
	})

	t.Run("an unset credential opens the recovery page, and saving it there resumes the deploy", func(t *testing.T) {
		root := clitest.SetUpVariablesFixtureWith(t, stripeDeclared, clitest.EnvDeclareOnlyScript)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), infisicalProduction)
		useFakeEnvSource(t, clitest.FakeEnvSource{
			Values: []clitest.FakeEnvSourceValue{{Key: "STRIPE_API_KEY", Value: "sk_live_from_infisical"}},
		})
		deps := clitest.NewDeps()
		terminalStdin(&deps)
		var mu sync.Mutex
		var opened []string
		recordBrowser(&deps, &opened, &mu)

		var out syncBuffer
		var stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &out)
		done := make(chan error, 1)
		go func() {
			done <- runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &out, &stderr, strings.NewReader(""))
		}()

		address, token := awaitEditorURL(t, &out, 1)
		setCell(t, address, token, "INFISICAL_CLIENT_ID", "client-id")
		setCell(t, address, token, "INFISICAL_CLIENT_SECRET", "client-secret")
		markDone(t, address, token)

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("runDeploy err = %v, want the deploy to resume on the env source's values; stdout=%s", err, out.String())
			}
		case <-time.After(60 * time.Second):
			t.Fatal("runDeploy never returned after the credentials were saved")
		}
		if !strings.Contains(out.String(), "Deployed") {
			t.Errorf("stdout = %q, want the resumed deploy to have completed", out.String())
		}
	})

	t.Run("a writable env source is handed the keys it lacks, empty, for a human to fill", func(t *testing.T) {
		root := setUpInfisicalFixture(t, strings.Replace(infisicalProduction, `environment: "prod",`, `environment: "prod", write: "missing",`, 1), clitest.FakeEnvSource{})

		var stdout, stderr bytes.Buffer
		deps := clitest.NewDeps()
		clitest.AttachTerminalSink(deps, &stdout)
		if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err == nil {
			t.Fatal("runDeploy err = nil, want the variables check to refuse: an empty key is still unset")
		}
		registrations, err := clitest.LoadFakeRegistrations()
		if err != nil {
			t.Fatal(err)
		}
		created := registrations[clitest.FakeRegistrationKey(environmentv1.Tier_TIER_PRODUCTION, clitest.FixtureSlug)].Created
		if len(created) != 1 || created[0].Key != "STRIPE_API_KEY" || created[0].Value != "" {
			t.Fatalf("created = %+v, want STRIPE_API_KEY created empty", created)
		}
		if out := stdout.String(); !strings.Contains(out, "Created STRIPE_API_KEY empty in infisical:p-1/prod") {
			t.Errorf("stdout = %q, want the creation reported", out)
		}
	})

	t.Run("a dry run creates nothing in the env source", func(t *testing.T) {
		root := setUpInfisicalFixture(t, strings.Replace(infisicalProduction, `environment: "prod",`, `environment: "prod", write: "missing",`, 1), clitest.FakeEnvSource{})

		var stdout, stderr bytes.Buffer
		deps := clitest.NewDeps()
		clitest.AttachTerminalSink(deps, &stdout)
		_ = runDeploy(context.Background(), deps, root, deployOptions{yes: true, dry: true}, &stdout, &stderr, strings.NewReader(""))
		registrations, err := clitest.LoadFakeRegistrations()
		if err != nil {
			t.Fatal(err)
		}
		registration, registered := registrations[clitest.FakeRegistrationKey(environmentv1.Tier_TIER_PRODUCTION, clitest.FixtureSlug)]
		if !registered || !strings.Contains(stdout.String(), "STRIPE_API_KEY") {
			t.Fatalf("registered = %v, stdout = %q, want the dry run to have read the env source and refused", registered, stdout.String())
		}
		if len(registration.Created) != 0 {
			t.Errorf("created = %+v, want a dry run to write nothing into the env source", registration.Created)
		}
	})

	t.Run("exec runs where ocel deploys, and the provider is handed what it printed", func(t *testing.T) {
		root := clitest.SetUpVariablesFixtureWith(t, stripeDeclared, clitest.EnvDeclareOnlyScript)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), strings.Replace(infisicalProduction,
			`production: { infisical: { project: "p-1", environment: "prod", auth: { universal: { clientId: { $env: "INFISICAL_CLIENT_ID" }, clientSecret: { $env: "INFISICAL_CLIENT_SECRET" } } } } },`,
			`production: { exec: { command: ["sh", "-c", "printf 'STRIPE_API_KEY=sk_from_exec'"], format: "dotenv" } },`, 1))

		var stdout, stderr bytes.Buffer
		deps := clitest.NewDeps()
		clitest.AttachTerminalSink(deps, &stdout)
		if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s", err, stdout.String())
		}
		store, err := clitest.LoadFakeStore()
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, cell := range store {
			latest := cell.Versions[len(cell.Versions)-1]
			found = found || (cell.Coordinate.Key == "STRIPE_API_KEY" && latest.Value == "sk_from_exec" && latest.EnvSource == "exec")
		}
		if !found {
			t.Errorf("store = %+v, want STRIPE_API_KEY copied from exec", store)
		}
	})

	t.Run("a tier back on builtin forgets the env source it read before", func(t *testing.T) {
		root := setUpInfisicalFixture(t, infisicalProduction, clitest.FakeEnvSource{
			Values: []clitest.FakeEnvSourceValue{{Key: "STRIPE_API_KEY", Value: "sk"}},
		})
		var first, stderr bytes.Buffer
		deps := clitest.NewDeps()
		clitest.AttachTerminalSink(deps, &first)
		if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &first, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s", err, first.String())
		}
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "`+clitest.FixtureSlug+`",
  provider: { aws: {} },
  domains: { preview: "*.preview.acme.com" },
};
`)

		var second bytes.Buffer
		deps = clitest.NewDeps()
		clitest.AttachTerminalSink(deps, &second)
		if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &second, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s", err, second.String())
		}
		registrations, err := clitest.LoadFakeRegistrations()
		if err != nil {
			t.Fatal(err)
		}
		if _, registered := registrations[clitest.FakeRegistrationKey(environmentv1.Tier_TIER_PRODUCTION, clitest.FixtureSlug)]; registered {
			t.Errorf("registrations = %+v, want production's registration forgotten", registrations)
		}
	})
}
