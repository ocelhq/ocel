package cloudflare

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/option"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func seedBootstrapBundles(t *testing.T, store, writer string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	storePath := filepath.Join(dir, "store.js")
	writerPath := filepath.Join(dir, "writer.js")
	for path, content := range map[string]string{storePath: store, writerPath: writer} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write bundle %s: %v", path, err)
		}
	}
	t.Setenv(edge.EnvStoreWorkerBundles, fmt.Sprintf(`{"cloudflare":%q}`, storePath))
	t.Setenv(edge.EnvISRWriterWorkerBundles, fmt.Sprintf(`{"cloudflare":%q}`, writerPath))
	return storePath, writerPath
}

func bootstrapMock(t *testing.T, held bool) *cfMock {
	t.Helper()
	t.Setenv(envAccountID, "acct")
	t.Setenv(envAPIToken, "tok")
	m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
	if held {
		m.existingBuckets = []string{cacheStoreName(edge.ClassProduction)}
		m.existingTokens = []map[string]any{{"id": "token-1", "name": cacheStoreName(edge.ClassProduction)}}
	}
	return m
}

func productionPlan(action edge.PlanAction, reason string) []edge.PlanChange {
	changes := []edge.PlanChange{
		{Kind: kindR2Bucket, Name: cacheStoreName(edge.ClassProduction), Action: action, Reason: reason},
		{Kind: kindAPIToken, Name: cacheStoreName(edge.ClassProduction), Action: action, Reason: reason},
	}
	for _, script := range []string{sharedStoreScriptName, isrWriterScriptName} {
		changes = append(changes,
			edge.PlanChange{Kind: kindWorker, Name: script, Action: action, Reason: reason},
			edge.PlanChange{Kind: kindWorkerSecret, Name: script + "/" + bootstrapSecretBinding, Action: action, Reason: reason},
			edge.PlanChange{Kind: kindWorkerSubdomain, Name: script, Action: action, Reason: reason},
		)
	}
	return changes
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
		if held, ok := b.(map[string]any); ok && held["type"] == kind {
			continue
		}
		kept = append(kept, b)
	}
	if len(kept) == len(bindings) {
		t.Fatalf("worker %q carries no %s binding to strip; it has %v", script, kind, bindings)
	}
	m.scriptSettings[script]["bindings"] = kept
}

func planner(t *testing.T, m *cfMock) edge.BootstrapPlanner {
	t.Helper()
	planner, ok := any(m.provider(t)).(edge.BootstrapPlanner)
	if !ok {
		t.Fatal("cloudflare provider does not implement edge.BootstrapPlanner")
	}
	return planner
}

func TestPlanBootstrap(t *testing.T) {
	t.Run("an untouched account creates every resource", func(t *testing.T) {
		seedBootstrapBundles(t, "export default {}", "export default {writer:1}")
		m := bootstrapMock(t, false)

		changes, err := planner(t, m).PlanBootstrap(t.Context(), edge.ClassProduction)
		if err != nil {
			t.Fatalf("PlanBootstrap: %v", err)
		}
		if want := productionPlan(edge.PlanCreate, ""); !reflect.DeepEqual(changes, want) {
			t.Errorf("plan = %+v, want %+v", changes, want)
		}
	})

	t.Run("a bootstrapped account keeps every resource", func(t *testing.T) {
		seedBootstrapBundles(t, "export default {}", "export default {writer:1}")
		m := bootstrapMock(t, true)
		p := m.provider(t)
		if _, err := p.Bootstrap(t.Context(), edge.ClassProduction); err != nil {
			t.Fatalf("Bootstrap: %v", err)
		}

		changes, err := p.PlanBootstrap(t.Context(), edge.ClassProduction)
		if err != nil {
			t.Fatalf("PlanBootstrap: %v", err)
		}
		if want := productionPlan(edge.PlanKeep, reasonCurrent); !reflect.DeepEqual(changes, want) {
			t.Errorf("plan = %+v, want %+v", changes, want)
		}
	})

	t.Run("a rebuilt bundle updates only the worker that drifted", func(t *testing.T) {
		storePath, _ := seedBootstrapBundles(t, "export default {}", "export default {writer:1}")
		m := bootstrapMock(t, true)
		p := m.provider(t)
		if _, err := p.Bootstrap(t.Context(), edge.ClassProduction); err != nil {
			t.Fatalf("Bootstrap: %v", err)
		}
		if err := os.WriteFile(storePath, []byte("export default {rebuilt:1}"), 0o600); err != nil {
			t.Fatalf("rewrite bundle: %v", err)
		}

		changes, err := p.PlanBootstrap(t.Context(), edge.ClassProduction)
		if err != nil {
			t.Fatalf("PlanBootstrap: %v", err)
		}
		if want := driftedWorker(sharedStoreScriptName, reasonScriptDrift); !reflect.DeepEqual(changes, want) {
			t.Errorf("plan = %+v, want %+v", changes, want)
		}
	})

	t.Run("a compatibility bump plans and applies an update to a script that has not changed", func(t *testing.T) {
		seedBootstrapBundles(t, "export default {}", "export default {writer:1}")
		m := bootstrapMock(t, true)
		p := m.provider(t)
		if _, err := p.Bootstrap(t.Context(), edge.ClassProduction); err != nil {
			t.Fatalf("Bootstrap: %v", err)
		}
		uploads := len(m.putScripts)
		m.scriptSettings[sharedStoreScriptName]["compatibility_date"] = "2020-01-01"

		changes, err := p.PlanBootstrap(t.Context(), edge.ClassProduction)
		if err != nil {
			t.Fatalf("PlanBootstrap: %v", err)
		}
		if !reflect.DeepEqual(changes, driftedWorker(sharedStoreScriptName, reasonMetadataDrift)) {
			t.Errorf("plan = %+v, want the store worker updated for metadata drift", changes)
		}

		if _, err := p.Bootstrap(t.Context(), edge.ClassProduction); err != nil {
			t.Fatalf("re-run Bootstrap: %v", err)
		}
		if got := m.putScripts[uploads:]; !reflect.DeepEqual(got, []string{sharedStoreScriptName}) {
			t.Errorf("uploads = %v, want the store worker alone re-uploaded", got)
		}
		if got := m.scriptSettings[sharedStoreScriptName]["compatibility_date"]; got != compatDate {
			t.Errorf("deployed compatibility date = %v, want this build's %q", got, compatDate)
		}
	})

	t.Run("a binding the deployed worker no longer carries plans as an update", func(t *testing.T) {
		seedBootstrapBundles(t, "export default {}", "export default {writer:1}")
		m := bootstrapMock(t, true)
		p := m.provider(t)
		if _, err := p.Bootstrap(t.Context(), edge.ClassProduction); err != nil {
			t.Fatalf("Bootstrap: %v", err)
		}
		uploads := len(m.putScripts)
		stripBinding(t, m, isrWriterScriptName, "r2_bucket")

		changes, err := p.PlanBootstrap(t.Context(), edge.ClassProduction)
		if err != nil {
			t.Fatalf("PlanBootstrap: %v", err)
		}
		if !reflect.DeepEqual(changes, driftedWorker(isrWriterScriptName, reasonMetadataDrift)) {
			t.Errorf("plan = %+v, want the isr writer updated for the binding it lost", changes)
		}

		if _, err := p.Bootstrap(t.Context(), edge.ClassProduction); err != nil {
			t.Fatalf("re-run Bootstrap: %v", err)
		}
		if got := m.putScripts[uploads:]; !reflect.DeepEqual(got, []string{isrWriterScriptName}) {
			t.Errorf("uploads = %v, want the isr writer alone re-uploaded", got)
		}
		if len(bindingsByType(uploadedMetadata(t, m, isrWriterScriptName), "r2_bucket")) != 1 {
			t.Error("the re-upload does not restore the cache store binding it was missing")
		}
	})

	t.Run("an unset account id is an error", func(t *testing.T) {
		t.Setenv(envAccountID, "")
		t.Setenv(envAPIToken, "tok")
		if _, err := New().(edge.BootstrapPlanner).PlanBootstrap(t.Context(), edge.ClassProduction); err == nil {
			t.Fatal("PlanBootstrap without an account id err = nil, want an error")
		}
	})

	t.Run("an unset api token is an error", func(t *testing.T) {
		t.Setenv(envAccountID, "acct")
		t.Setenv(envAPIToken, "")
		if _, err := New().(edge.BootstrapPlanner).PlanBootstrap(t.Context(), edge.ClassProduction); err == nil {
			t.Fatal("PlanBootstrap without an api token err = nil, want an error")
		}
	})
}

func TestBootstrapConverges(t *testing.T) {
	t.Run("a second run writes nothing and reoffers no credential", func(t *testing.T) {
		seedBootstrapBundles(t, "export default {}", "export default {writer:1}")
		m := bootstrapMock(t, true)
		p := m.provider(t)

		first, err := p.Bootstrap(t.Context(), edge.ClassProduction)
		if err != nil {
			t.Fatalf("Bootstrap: %v", err)
		}
		for _, offer := range first.Offers {
			if offer.Kind == edge.OfferCacheStore {
				continue
			}
			if credOf(t, offer) == "" {
				t.Errorf("first Bootstrap offered %q with no bootstrap credential", offer.Kind)
			}
		}
		puts, subdomains := len(m.putScripts), len(m.subdomainCalls)

		second, err := p.Bootstrap(t.Context(), edge.ClassProduction)
		if err != nil {
			t.Fatalf("re-run Bootstrap: %v", err)
		}
		if len(m.putScripts) != puts {
			t.Errorf("re-run uploaded %v, want no upload", m.putScripts[puts:])
		}
		if len(m.subdomainCalls) != subdomains {
			t.Errorf("re-run set %v subdomains, want none", m.subdomainCalls[subdomains:])
		}
		if len(m.createdBuckets) != 0 {
			t.Errorf("created buckets = %v, want none", m.createdBuckets)
		}
		for _, offer := range second.Offers {
			if offer.Kind == edge.OfferCacheStore {
				continue
			}
			if cred := credOf(t, offer); cred != "" {
				t.Errorf("re-run offered %q a fresh credential %q, want the stored one left alone", offer.Kind, cred)
			}
		}
		if !reflect.DeepEqual(endpoints(first), endpoints(second)) {
			t.Errorf("endpoints = %v, want %v", endpoints(second), endpoints(first))
		}
	})

	t.Run("a drifted bundle is re-uploaded inheriting the standing secret", func(t *testing.T) {
		storePath, _ := seedBootstrapBundles(t, "export default {}", "export default {writer:1}")
		m := bootstrapMock(t, true)
		p := m.provider(t)
		if _, err := p.Bootstrap(t.Context(), edge.ClassProduction); err != nil {
			t.Fatalf("Bootstrap: %v", err)
		}
		if err := os.WriteFile(storePath, []byte("export default {rebuilt:1}"), 0o600); err != nil {
			t.Fatalf("rewrite bundle: %v", err)
		}

		out, err := p.Bootstrap(t.Context(), edge.ClassProduction)
		if err != nil {
			t.Fatalf("re-run Bootstrap: %v", err)
		}
		if got := m.putScripts; !reflect.DeepEqual(got, []string{sharedStoreScriptName, isrWriterScriptName, sharedStoreScriptName}) {
			t.Errorf("uploads = %v, want the drifted store worker alone re-uploaded", got)
		}

		meta := uploadedMetadata(t, m, sharedStoreScriptName)
		if len(bindingsByType(meta, "secret_text")) != 0 {
			t.Errorf("re-upload carries a fresh secret binding: %v", bindingsByType(meta, "secret_text"))
		}
		inherited := bindingsByType(meta, inheritedBindingType)
		if len(inherited) != 1 || inherited[0]["name"] != bootstrapSecretBinding {
			t.Errorf("inherited bindings = %v, want %s alone", inherited, bootstrapSecretBinding)
		}
		if !slices.Contains(m.scriptSecrets[sharedStoreScriptName], bootstrapSecretBinding) {
			t.Errorf("secrets after re-upload = %v, want %s held", m.scriptSecrets[sharedStoreScriptName], bootstrapSecretBinding)
		}
		for _, offer := range out.Offers {
			if offer.Kind == edge.OfferDeploymentsStore && credOf(t, offer) != "" {
				t.Error("a drift re-upload minted a fresh credential, orphaning the stored one")
			}
		}
	})
}

func credOf(t *testing.T, offer edge.Offer) string {
	t.Helper()
	switch offer.Kind {
	case edge.OfferDeploymentsStore:
		return offer.Values[edge.OfferKeyStoreBootstrapCred]
	case edge.OfferISRWriter:
		return offer.Values[edge.OfferKeyISRWriterBootstrapCred]
	default:
		t.Fatalf("offer %q carries no bootstrap credential", offer.Kind)
		return ""
	}
}

func endpoints(out edge.BootstrapOutput) map[edge.OfferKind]string {
	found := map[edge.OfferKind]string{}
	for _, offer := range out.Offers {
		switch offer.Kind {
		case edge.OfferDeploymentsStore:
			found[offer.Kind] = offer.Values[edge.OfferKeyStoreEndpoint]
		case edge.OfferISRWriter:
			found[offer.Kind] = offer.Values[edge.OfferKeyISRWriterEndpoint]
		}
	}
	return found
}

func TestDeployedScriptWithoutTheModuleItExpected(t *testing.T) {
	body, contentType, err := buildScriptMultipart(edge.Worker{Main: edge.WorkerModule{
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
	p := &provider{client: cf.NewClient(option.WithBaseURL(srv.URL+"/"), option.WithAPIToken("test"))}

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
