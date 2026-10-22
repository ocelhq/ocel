package cloudflare

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/option"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

func seedBootstrapBundle(t *testing.T, store string) {
	t.Helper()
	prior := releasesStoreBundle
	releasesStoreBundle = []byte(store)
	t.Cleanup(func() { releasesStoreBundle = prior })
}

func bootstrapMock(t *testing.T) *cfMock {
	t.Helper()
	t.Setenv(envAccountID, "acct")
	t.Setenv(envAPIToken, "tok")
	return &cfMock{zoneID: "zone1", zoneName: "app.com"}
}

func productionPlan(action edge.PlanAction, reason string) []edge.PlanChange {
	return []edge.PlanChange{
		{Kind: kindWorker, Name: sharedStoreScriptName, Action: action, Reason: reason},
		{Kind: kindWorkerSecret, Name: sharedStoreScriptName + "/" + bootstrapSecretBinding, Action: action, Reason: reason},
		{Kind: kindWorkerSubdomain, Name: sharedStoreScriptName, Action: action, Reason: reason},
	}
}

func driftedWorker(script, reason string) []edge.PlanChange {
	want := productionPlan(edge.PlanKeep, reasonCurrent)
	for i, change := range want {
		if change.Kind == kindWorker && change.Name == script {
			want[i].Action, want[i].Reason = edge.PlanUpdate, reason
		}
	}
	return want
}

func stripBinding(t *testing.T, m *cfMock, script, kind string) {
	t.Helper()
	bindings, _ := m.scriptSettings[script]["bindings"].([]any)
	kept := make([]any, 0, len(bindings))
	for _, b := range bindings {
		if declared, ok := b.(map[string]any); ok && declared["type"] == kind {
			continue
		}
		kept = append(kept, b)
	}
	if len(kept) == len(bindings) {
		t.Fatalf("worker %q has no %s binding to strip; it has %v", script, kind, bindings)
	}
	m.scriptSettings[script]["bindings"] = kept
}

func planner(t *testing.T, m *cfMock) func(context.Context, environment.Tier) ([]edge.PlanChange, error) {
	t.Helper()
	plan := m.provider(t).Hooks().PlanBootstrap
	if plan == nil {
		t.Fatal("the cloudflare edge plans no bootstrap of its own")
	}
	return plan
}

func TestPlanBootstrap(t *testing.T) {
	t.Run("an untouched account creates every resource", func(t *testing.T) {
		seedBootstrapBundle(t, "export default {}")
		m := bootstrapMock(t)

		changes, err := planner(t, m)(t.Context(), environment.TierProduction)
		if err != nil {
			t.Fatalf("PlanBootstrap: %v", err)
		}
		if want := productionPlan(edge.PlanCreate, ""); !reflect.DeepEqual(changes, want) {
			t.Errorf("plan = %+v, want %+v", changes, want)
		}
	})

	t.Run("a bootstrapped account keeps every resource", func(t *testing.T) {
		seedBootstrapBundle(t, "export default {}")
		m := bootstrapMock(t)
		p := m.provider(t)
		if _, err := p.Bootstrap(t.Context(), environment.TierProduction); err != nil {
			t.Fatalf("Bootstrap: %v", err)
		}

		changes, err := p.planBootstrap(t.Context(), environment.TierProduction)
		if err != nil {
			t.Fatalf("PlanBootstrap: %v", err)
		}
		if want := productionPlan(edge.PlanKeep, reasonCurrent); !reflect.DeepEqual(changes, want) {
			t.Errorf("plan = %+v, want %+v", changes, want)
		}
	})

	t.Run("a rebuilt bundle updates only the worker that drifted", func(t *testing.T) {
		seedBootstrapBundle(t, "export default {}")
		m := bootstrapMock(t)
		p := m.provider(t)
		if _, err := p.Bootstrap(t.Context(), environment.TierProduction); err != nil {
			t.Fatalf("Bootstrap: %v", err)
		}
		releasesStoreBundle = []byte("export default {rebuilt:1}")

		changes, err := p.planBootstrap(t.Context(), environment.TierProduction)
		if err != nil {
			t.Fatalf("PlanBootstrap: %v", err)
		}
		if want := driftedWorker(sharedStoreScriptName, reasonScriptDrift); !reflect.DeepEqual(changes, want) {
			t.Errorf("plan = %+v, want %+v", changes, want)
		}
	})

	t.Run("a store that does not carry its origin's tag plans and applies an update", func(t *testing.T) {
		seedBootstrapBundle(t, "export default {}")
		m := bootstrapMock(t)
		p := m.provider(t)
		if _, err := p.Bootstrap(t.Context(), environment.TierProduction); err != nil {
			t.Fatalf("Bootstrap: %v", err)
		}
		delete(m.scriptSettings[sharedStoreScriptName], "tags")

		changes, err := p.planBootstrap(t.Context(), environment.TierProduction)
		if err != nil {
			t.Fatalf("PlanBootstrap: %v", err)
		}
		if !reflect.DeepEqual(changes, driftedWorker(sharedStoreScriptName, reasonMetadataDrift)) {
			t.Errorf("plan = %+v, want the store worker updated so it names the origin that owns it", changes)
		}
	})

	t.Run("a compatibility bump plans and applies an update to a script that has not changed", func(t *testing.T) {
		seedBootstrapBundle(t, "export default {}")
		m := bootstrapMock(t)
		p := m.provider(t)
		if _, err := p.Bootstrap(t.Context(), environment.TierProduction); err != nil {
			t.Fatalf("Bootstrap: %v", err)
		}
		uploads := len(m.putScripts)
		m.scriptSettings[sharedStoreScriptName]["compatibility_date"] = "2020-01-01"

		changes, err := p.planBootstrap(t.Context(), environment.TierProduction)
		if err != nil {
			t.Fatalf("PlanBootstrap: %v", err)
		}
		if !reflect.DeepEqual(changes, driftedWorker(sharedStoreScriptName, reasonMetadataDrift)) {
			t.Errorf("plan = %+v, want the store worker updated for metadata drift", changes)
		}

		if _, err := p.Bootstrap(t.Context(), environment.TierProduction); err != nil {
			t.Fatalf("re-run Bootstrap: %v", err)
		}
		if got := m.putScripts[uploads:]; !reflect.DeepEqual(got, []string{sharedStoreScriptName}) {
			t.Errorf("uploads = %v, want the store worker alone re-uploaded", got)
		}
		if got := m.scriptSettings[sharedStoreScriptName]["compatibility_date"]; got != compatDate {
			t.Errorf("deployed compatibility date = %v, want this build's %q", got, compatDate)
		}
	})

	t.Run("a binding the deployed worker no longer has plans as an update", func(t *testing.T) {
		seedBootstrapBundle(t, "export default {}")
		m := bootstrapMock(t)
		p := m.provider(t)
		if _, err := p.Bootstrap(t.Context(), environment.TierProduction); err != nil {
			t.Fatalf("Bootstrap: %v", err)
		}
		uploads := len(m.putScripts)
		stripBinding(t, m, sharedStoreScriptName, durableObjectBindingType)

		changes, err := p.planBootstrap(t.Context(), environment.TierProduction)
		if err != nil {
			t.Fatalf("PlanBootstrap: %v", err)
		}
		if !reflect.DeepEqual(changes, driftedWorker(sharedStoreScriptName, reasonMetadataDrift)) {
			t.Errorf("plan = %+v, want the releases store updated for the binding it lost", changes)
		}

		if _, err := p.Bootstrap(t.Context(), environment.TierProduction); err != nil {
			t.Fatalf("re-run Bootstrap: %v", err)
		}
		if got := m.putScripts[uploads:]; !reflect.DeepEqual(got, []string{sharedStoreScriptName}) {
			t.Errorf("uploads = %v, want the releases store alone re-uploaded", got)
		}
		if len(bindingsByType(uploadedMetadata(t, m, sharedStoreScriptName), durableObjectBindingType)) != 1 {
			t.Error("the re-upload does not restore the durable object binding it was missing")
		}
	})

	t.Run("an unset account id is an error", func(t *testing.T) {
		t.Setenv(envAccountID, "")
		t.Setenv(envAPIToken, "tok")
		if _, err := New("ocel", fixedOrigin(testOrigin), nil, Options{}).Hooks().PlanBootstrap(t.Context(), environment.TierProduction); err == nil {
			t.Fatal("PlanBootstrap without an account id err = nil, want an error")
		}
	})

	t.Run("an unset api token is an error", func(t *testing.T) {
		t.Setenv(envAccountID, "acct")
		t.Setenv(envAPIToken, "")
		if _, err := New("ocel", fixedOrigin(testOrigin), nil, Options{}).Hooks().PlanBootstrap(t.Context(), environment.TierProduction); err == nil {
			t.Fatal("PlanBootstrap without an api token err = nil, want an error")
		}
	})
}

func TestBootstrapConverges(t *testing.T) {
	t.Run("a second run writes nothing and reoffers no credential", func(t *testing.T) {
		seedBootstrapBundle(t, "export default {}")
		m := bootstrapMock(t)
		p := m.provider(t)

		first, err := p.Bootstrap(t.Context(), environment.TierProduction)
		if err != nil {
			t.Fatalf("Bootstrap: %v", err)
		}
		if releasesStoreOf(t, first).BootstrapCredential == "" {
			t.Error("first Bootstrap offered the releases store with no bootstrap credential")
		}
		puts, subdomains := len(m.putScripts), len(m.subdomainCalls)

		second, err := p.Bootstrap(t.Context(), environment.TierProduction)
		if err != nil {
			t.Fatalf("re-run Bootstrap: %v", err)
		}
		if len(m.putScripts) != puts {
			t.Errorf("re-run uploaded %v, want no upload", m.putScripts[puts:])
		}
		if len(m.subdomainCalls) != subdomains {
			t.Errorf("re-run set %v subdomains, want none", m.subdomainCalls[subdomains:])
		}
		if cred := releasesStoreOf(t, second).BootstrapCredential; cred != "" {
			t.Errorf("re-run offered the releases store a fresh credential %q, want the stored one left alone", cred)
		}
		if got, want := releasesStoreOf(t, second).Endpoint, releasesStoreOf(t, first).Endpoint; got != want {
			t.Errorf("endpoint = %q, want %q", got, want)
		}
	})

	t.Run("a drifted bundle is re-uploaded inheriting the existing secret", func(t *testing.T) {
		seedBootstrapBundle(t, "export default {}")
		m := bootstrapMock(t)
		p := m.provider(t)
		if _, err := p.Bootstrap(t.Context(), environment.TierProduction); err != nil {
			t.Fatalf("Bootstrap: %v", err)
		}
		releasesStoreBundle = []byte("export default {rebuilt:1}")

		out, err := p.Bootstrap(t.Context(), environment.TierProduction)
		if err != nil {
			t.Fatalf("re-run Bootstrap: %v", err)
		}
		if got := m.putScripts; !reflect.DeepEqual(got, []string{sharedStoreScriptName, sharedStoreScriptName}) {
			t.Errorf("uploads = %v, want the drifted store worker alone re-uploaded", got)
		}

		meta := uploadedMetadata(t, m, sharedStoreScriptName)
		if len(bindingsByType(meta, "secret_text")) != 0 {
			t.Errorf("re-upload includes a fresh secret binding: %v", bindingsByType(meta, "secret_text"))
		}
		inherited := bindingsByType(meta, inheritedBindingType)
		if len(inherited) != 1 || inherited[0]["name"] != bootstrapSecretBinding {
			t.Errorf("inherited bindings = %v, want %s alone", inherited, bootstrapSecretBinding)
		}
		if !slices.Contains(m.scriptSecrets[sharedStoreScriptName], bootstrapSecretBinding) {
			t.Errorf("secrets after re-upload = %v, want %s kept", m.scriptSecrets[sharedStoreScriptName], bootstrapSecretBinding)
		}
		if releasesStoreOf(t, out).BootstrapCredential != "" {
			t.Error("a drift re-upload minted a fresh credential, orphaning the stored one")
		}
	})
}

func missingSecret(script string) []edge.PlanChange {
	want := productionPlan(edge.PlanKeep, reasonCurrent)
	for i, change := range want {
		if change.Kind == kindWorkerSecret && change.Name == script+"/"+bootstrapSecretBinding {
			want[i].Action, want[i].Reason = edge.PlanCreate, ""
		}
	}
	return want
}

func TestBootstrapWithTheCredentialGone(t *testing.T) {
	t.Run("a worker that lost its secret is planned and repaired by setting the secret alone", func(t *testing.T) {
		seedBootstrapBundle(t, "export default {}")
		m := bootstrapMock(t)
		p := m.provider(t)
		if _, err := p.Bootstrap(t.Context(), environment.TierProduction); err != nil {
			t.Fatalf("Bootstrap: %v", err)
		}
		uploads := len(m.putScripts)
		m.scriptSecrets[sharedStoreScriptName] = nil

		changes, err := p.planBootstrap(t.Context(), environment.TierProduction)
		if err != nil {
			t.Fatalf("PlanBootstrap: %v", err)
		}
		if want := missingSecret(sharedStoreScriptName); !reflect.DeepEqual(changes, want) {
			t.Errorf("plan = %+v, want %+v", changes, want)
		}

		out, err := p.Bootstrap(t.Context(), environment.TierProduction)
		if err != nil {
			t.Fatalf("re-run Bootstrap: %v", err)
		}
		if got := m.putScripts[uploads:]; len(got) != 0 {
			t.Errorf("uploads = %v, want the deployed script left where it is", got)
		}
		if len(m.putSecrets) != 1 || m.putSecrets[0].script != sharedStoreScriptName || m.putSecrets[0].name != bootstrapSecretBinding {
			t.Errorf("secrets set = %+v, want %s alone on %s", m.putSecrets, bootstrapSecretBinding, sharedStoreScriptName)
		}
		if cred := releasesStoreOf(t, out).BootstrapCredential; cred != m.putSecrets[0].text {
			t.Errorf("offered credential = %q, want the %q that was set on the worker", cred, m.putSecrets[0].text)
		}

		replanned, err := p.planBootstrap(t.Context(), environment.TierProduction)
		if err != nil {
			t.Fatalf("PlanBootstrap after the secret was set: %v", err)
		}
		if want := productionPlan(edge.PlanKeep, reasonCurrent); !reflect.DeepEqual(replanned, want) {
			t.Errorf("plan after the secret was set = %+v, want everything kept", replanned)
		}
	})
}

func releasesStoreOf(t *testing.T, out edge.BootstrapOutput) edge.ReleasesStore {
	t.Helper()
	if out.ReleasesStore == nil {
		t.Fatal("Bootstrap offered no releases store")
	}
	return *out.ReleasesStore
}

func TestDeployedScriptWithoutTheModuleItExpected(t *testing.T) {
	body, contentType, err := buildScriptMultipart(workerScript{Main: workerModule{
		Name:        "worker.js",
		ContentType: "application/javascript+module",
		Content:     []byte("export default {}"),
	}}, "")
	if err != nil {
		t.Fatalf("buildScriptMultipart: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	p := &cloudflare{client: cf.NewClient(option.WithBaseURL(srv.URL+"/"), option.WithAPIToken("test"))}

	content, present, err := p.deployedScript(t.Context(), "acct", sharedStoreScriptName, "index.js")
	if err == nil {
		t.Fatalf("deployedScript = (%q, %v, nil), want a response missing the main module to be an error rather than drift", content, present)
	}
	for _, want := range []string{sharedStoreScriptName, "index.js"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %v does not name %q", err, want)
		}
	}
}
