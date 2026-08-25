package cloudflare

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

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
		want := productionPlan(edge.PlanKeep, reasonCurrent)
		for i, change := range want {
			if change.Kind == kindWorker && change.Name == sharedStoreScriptName {
				want[i].Action, want[i].Reason = edge.PlanUpdate, reasonScriptDrift
			}
		}
		if !reflect.DeepEqual(changes, want) {
			t.Errorf("plan = %+v, want %+v", changes, want)
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
		if !slicesContainsSecret(m.scriptSecrets[sharedStoreScriptName]) {
			t.Errorf("secrets after re-upload = %v, want %s held", m.scriptSecrets[sharedStoreScriptName], bootstrapSecretBinding)
		}
		for _, offer := range out.Offers {
			if offer.Kind == edge.OfferDeploymentsStore && credOf(t, offer) != "" {
				t.Error("a drift re-upload minted a fresh credential, orphaning the stored one")
			}
		}
	})
}

func slicesContainsSecret(names []string) bool {
	for _, name := range names {
		if name == bootstrapSecretBinding {
			return true
		}
	}
	return false
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
