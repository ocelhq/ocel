package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

const infisicalProduction = `
export default {
  slug: "` + clitest.FixtureSlug + `",
  provider: { aws: {} },
  domains: { preview: "*.preview.acme.com" },
  envSource: {
    production: { infisical: { project: "p-1", environment: "prod", auth: { universal: { clientId: { var: "INFISICAL_CLIENT_ID" }, clientSecret: { var: "INFISICAL_CLIENT_SECRET" } } } } },
  },
};
`

func useFakeSource(t *testing.T, values []clitest.FakeSourced) {
	t.Helper()
	raw, err := json.Marshal(clitest.FakeEnvSource{
		ID:      "infisical:p-1/prod",
		Values:  values,
		Links:   map[string]string{"": "https://infisical.example/p-1/prod"},
		Attempt: 1_700_000_000,
		Success: 1_700_000_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(clitest.FakeEnvSourceEnvVar, string(raw))
}

func TestADeployReadsItsTierFromTheEnvSourceFirst(t *testing.T) {
	t.Run("a value the source holds passes the gate", func(t *testing.T) {
		root := clitest.SetUpEnvGateFixtureWith(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`, clitest.EnvDeclareOnlyScript)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), infisicalProduction)
		useFakeSource(t, []clitest.FakeSourced{{Key: "STRIPE_API_KEY", Value: "sk_live_from_infisical"}})

		var stdout, stderr bytes.Buffer
		if err := runDeploy(context.Background(), clitest.NewDeps(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "Deployed") {
			t.Errorf("stdout = %q, want the deploy to complete on the value the source holds", stdout.String())
		}
	})

	t.Run("a value the source lacks refuses, naming where to set it", func(t *testing.T) {
		root := clitest.SetUpEnvGateFixtureWith(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`, clitest.EnvDeclareOnlyScript)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), infisicalProduction)
		useFakeSource(t, nil)

		var stdout, stderr bytes.Buffer
		err := runDeploy(context.Background(), clitest.NewDeps(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want the gate to refuse")
		}
		out := stdout.String()
		for _, want := range []string{"STRIPE_API_KEY", "set it in infisical:p-1/prod", "https://infisical.example/p-1/prod"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want %q", out, want)
			}
		}
		if strings.Contains(out, "ocel env set STRIPE_API_KEY") {
			t.Errorf("stdout = %q, want no `ocel env set` offered for a value the source owns", out)
		}
	})

	t.Run("what the source holds and nothing declares is drift, not a refusal", func(t *testing.T) {
		root := clitest.SetUpEnvGateFixtureWith(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`, clitest.EnvDeclareOnlyScript)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), infisicalProduction)
		useFakeSource(t, []clitest.FakeSourced{{Key: "STRIPE_API_KEY", Value: "sk"}, {Key: "RETIRED_TOKEN", Value: "old"}})

		var stdout, stderr bytes.Buffer
		if err := runDeploy(context.Background(), clitest.NewDeps(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s", err, stdout.String())
		}
		if out := stdout.String(); !strings.Contains(out, "RETIRED_TOKEN") || !strings.Contains(out, "nothing this project declares") {
			t.Errorf("stdout = %q, want RETIRED_TOKEN reported as drift", out)
		}
	})

	t.Run("a source that cannot be read stops the deploy before anything is built", func(t *testing.T) {
		root := clitest.SetUpEnvGateFixtureWith(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`, clitest.EnvDeclareOnlyScript)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), infisicalProduction)
		t.Setenv(clitest.FakeEnvSourceEnvVar, `{"id":"infisical:p-1/prod","error":"Infisical answered 503"}`)
		deps := clitest.NewDeps()
		built := false
		stubAppBuildRecorder(&deps, &built)

		var stdout, stderr bytes.Buffer
		err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil || built {
			t.Fatalf("runDeploy err = %v, built = %v, want an unreadable source to stop the deploy first", err, built)
		}
		if out := stdout.String() + err.Error(); !strings.Contains(out, "503") {
			t.Errorf("output = %q, want the source's failure named", out)
		}
	})

	t.Run("a writable source is handed the keys it lacks to fill in", func(t *testing.T) {
		root := clitest.SetUpEnvGateFixtureWith(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true,"description":"Stripe's secret key"}]`, clitest.EnvDeclareOnlyScript)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), strings.Replace(infisicalProduction, `environment: "prod",`, `environment: "prod", write: "missing",`, 1))
		useFakeSource(t, nil)

		var stdout, stderr bytes.Buffer
		if err := runDeploy(context.Background(), clitest.NewDeps(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err == nil {
			t.Fatal("runDeploy err = nil, want the gate to refuse: an empty key is still unset")
		}
		registrations, err := clitest.LoadFakeRegistrations()
		if err != nil {
			t.Fatal(err)
		}
		created := registrations["TIER_PRODUCTION "+clitest.FixtureSlug].Created
		if len(created) != 1 || created[0].Key != "STRIPE_API_KEY" || created[0].Value != "" {
			t.Fatalf("created = %+v, want STRIPE_API_KEY created empty for a human to fill", created)
		}
		if out := stdout.String(); !strings.Contains(out, "created STRIPE_API_KEY") {
			t.Errorf("stdout = %q, want the creation reported", out)
		}
	})
}
