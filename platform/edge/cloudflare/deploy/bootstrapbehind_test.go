package cloudflare

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func installBootstrap(t *testing.T, m *cfMock, tier environment.Tier) {
	t.Helper()
	planned, err := bootstrapWorkers("ocel", testOrigin, tier)
	if err != nil {
		t.Fatalf("bootstrapWorkers: %v", err)
	}
	if m.putBodies == nil {
		m.putBodies = map[string]putBody{}
	}
	if m.scriptSettings == nil {
		m.scriptSettings = map[string]map[string]any{}
	}
	if m.scriptSecrets == nil {
		m.scriptSecrets = map[string][]string{}
	}
	for _, b := range planned {
		content, contentType, err := buildDurableObjectScriptMultipart(b.worker, b.do, nil, nil)
		if err != nil {
			t.Fatalf("build %s: %v", b.scriptName, err)
		}
		m.putBodies[b.scriptName] = putBody{contentType: contentType, content: content}
		m.scriptSecrets[b.scriptName] = uploadedSecretNames(m.putBodies[b.scriptName], nil)
		m.scriptSettings[b.scriptName] = putSettings(m.putBodies[b.scriptName])
	}
}

func productionEntrySpec(endpoint string) edge.StackSpec {
	spec := testSpec(endpoint, "v1")
	return spec
}

func refusedNotReady(t *testing.T, err error) refusal.Refusal {
	t.Helper()
	var refused refusal.Refusal
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if refused.Code != refusal.CodeNotReady {
		t.Fatalf("refusal code = %v, want %v", refused.Code, refusal.CodeNotReady)
	}
	return refused
}

func bootstrapScriptName(t *testing.T, tier environment.Tier) string {
	t.Helper()
	planned, err := bootstrapWorkers("ocel", testOrigin, tier)
	if err != nil {
		t.Fatalf("bootstrapWorkers: %v", err)
	}
	if len(planned) != 1 {
		t.Fatalf("bootstrapWorkers = %d workers, want the releases store alone", len(planned))
	}
	return planned[0].scriptName
}

func TestADeployOntoABootstrapWhoseStoreIsBehindIsRefusedBeforeAnythingIsUploaded(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	seedBootstrapBundle(t, "store-v1")
	m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
	installBootstrap(t, m, environment.TierProduction)
	seedBootstrapBundle(t, "store-v2")

	store, _ := fakeStoreFor(t, "")
	var reached atomic.Int32
	handler := store.Config.Handler
	store.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		handler.ServeHTTP(w, r)
	})

	_, err := m.provider(t).Reconcile(t.Context(), productionEntrySpec(store.URL), edge.StackState{})

	refused := refusedNotReady(t, err)
	storeScript := bootstrapScriptName(t, environment.TierProduction)
	for _, want := range []string{provider.BootstrapCommand(environment.TierProduction), storeScript, "behind"} {
		if !strings.Contains(refused.Error(), want) {
			t.Errorf("refusal %q does not mention %q", refused.Error(), want)
		}
	}
	if len(m.putScripts) != 0 {
		t.Errorf("uploaded %v, want nothing", m.putScripts)
	}
	if reached.Load() != 0 {
		t.Errorf("the store received %d requests, want none", reached.Load())
	}
}

func TestADeployOntoABootstrapWithoutTheReleasesStoreIsRefused(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	seedBootstrapBundle(t, "store-v1")
	m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
	installBootstrap(t, m, environment.TierProduction)
	storeScript := bootstrapScriptName(t, environment.TierProduction)
	delete(m.putBodies, storeScript)
	delete(m.scriptSettings, storeScript)
	delete(m.scriptSecrets, storeScript)
	store := fakeStoreServer(t, "")

	_, err := m.provider(t).Reconcile(t.Context(), productionEntrySpec(store.URL), edge.StackState{})

	refused := refusedNotReady(t, err)
	for _, want := range []string{storeScript, "missing", provider.BootstrapCommand(environment.TierProduction)} {
		if !strings.Contains(refused.Error(), want) {
			t.Errorf("refusal %q does not mention %q", refused.Error(), want)
		}
	}
	if len(m.putScripts) != 0 {
		t.Errorf("uploaded %v, want nothing", m.putScripts)
	}
}

func TestADeployOntoABootstrapWhoseWorkerBindingsDriftedIsRefused(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	seedBootstrapBundle(t, "store-v1")
	m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
	installBootstrap(t, m, environment.TierProduction)
	storeScript := bootstrapScriptName(t, environment.TierProduction)
	stripBinding(t, m, storeScript, durableObjectBindingType)
	store := fakeStoreServer(t, "")

	_, err := m.provider(t).Reconcile(t.Context(), productionEntrySpec(store.URL), edge.StackState{})

	refused := refusedNotReady(t, err)
	if !strings.Contains(refused.Error(), "behind") {
		t.Errorf("refusal %q does not say the worker is behind", refused.Error())
	}
}

func TestADeployOntoACurrentBootstrapUploadsItsEntryWorker(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	seedBootstrapBundle(t, "store-v1")
	m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
	installBootstrap(t, m, environment.TierProduction)
	store := fakeStoreServer(t, "")
	spec := productionEntrySpec(store.URL)

	if _, err := m.provider(t).Reconcile(t.Context(), spec, edge.StackState{}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if name := entryOf(t, spec).name; !slices.Contains(m.putScripts, name) {
		t.Errorf("uploaded %v, want %q among them", m.putScripts, name)
	}
}

func TestTheSharedPreviewEntryIsNotUploadedOntoABootstrapThatIsBehind(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	seedBootstrapBundle(t, "store-v1")
	m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
	installBootstrap(t, m, environment.TierPreview)
	seedBootstrapBundle(t, "store-v2")

	_, err := m.provider(t).ReconcilePreviewWildcard(t.Context(), previewWildcardSpec())

	refused := refusedNotReady(t, err)
	if !strings.Contains(refused.Error(), provider.BootstrapCommand(environment.TierPreview)) {
		t.Errorf("refusal %q does not name the preview bootstrap command", refused.Error())
	}
	if slices.Contains(m.putScripts, previewEntryScriptName) {
		t.Errorf("uploaded %v, want no shared preview entry", m.putScripts)
	}
}

func TestAPruneOnlyReconcileIsNotGatedOnTheBootstrap(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
	store := fakeStoreServer(t, "")

	if _, err := m.provider(t).Reconcile(t.Context(), pruneOnlySpec(store.URL, "v1"), edge.StackState{}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

func TestTheBootstrapRefusalNamesTheCallerAndWhatToRerun(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	seedBootstrapBundle(t, "store-v1")

	cases := []struct {
		name    string
		tier    environment.Tier
		trigger func(t *testing.T, m *cfMock) error
		want    string
	}{
		{
			name: "a deploy with the worker missing",
			tier: environment.TierProduction,
			trigger: func(t *testing.T, m *cfMock) error {
				script := bootstrapScriptName(t, environment.TierProduction)
				delete(m.putBodies, script)
				delete(m.scriptSettings, script)
				store := fakeStoreServer(t, "")
				_, err := m.provider(t).Reconcile(t.Context(), productionEntrySpec(store.URL), edge.StackState{})
				return err
			},
			want: "and the entry worker this deploy uploads calls it, so serving it would fail every request it routes.\nRe-run `" +
				provider.BootstrapCommand(environment.TierProduction) + "` to install this build's, then deploy again",
		},
		{
			name: "the preview wildcard with the worker behind",
			tier: environment.TierPreview,
			trigger: func(t *testing.T, m *cfMock) error {
				seedBootstrapBundle(t, "store-v2")
				_, err := m.provider(t).ReconcilePreviewWildcard(t.Context(), previewWildcardSpec())
				return err
			},
			want: "and the shared preview entry worker calls it, so serving it would fail every request it routes.\nRe-run `" +
				provider.BootstrapCommand(environment.TierPreview) + "` to install this build's, then run this again",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seedBootstrapBundle(t, "store-v1")
			m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
			installBootstrap(t, m, tc.tier)

			refused := refusedNotReady(t, tc.trigger(t, m))

			if !strings.Contains(refused.Error(), tc.want) {
				t.Errorf("refusal %q does not contain %q", refused.Error(), tc.want)
			}
		})
	}
}
