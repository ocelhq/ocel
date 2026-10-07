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
	planned, err := bootstrapWorkers("ocel", tier)
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
	program := *spec.Program
	program.Name = "ocel-acme-web"
	program.Worker = testStoreWorker()
	spec.Program = &program
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

func bootstrapScriptNames(t *testing.T, tier environment.Tier) (store, writer string) {
	t.Helper()
	planned, err := bootstrapWorkers("ocel", tier)
	if err != nil {
		t.Fatalf("bootstrapWorkers: %v", err)
	}
	return planned[0].scriptName, planned[1].scriptName
}

func TestADeployOntoABootstrapWhoseStoreIsBehindIsRefusedBeforeAnythingIsUploaded(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	seedBootstrapBundles(t, "store-v1", "writer-v1")
	m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
	installBootstrap(t, m, environment.TierProduction)
	seedBootstrapBundles(t, "store-v2", "writer-v1")

	store, _ := fakeStoreFor(t, "")
	var reached atomic.Int32
	handler := store.Config.Handler
	store.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		handler.ServeHTTP(w, r)
	})

	_, err := m.provider(t).Reconcile(t.Context(), productionEntrySpec(store.URL), edge.StackState{})

	refused := refusedNotReady(t, err)
	storeScript, _ := bootstrapScriptNames(t, environment.TierProduction)
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

func TestADeployOntoABootstrapWhoseStoreIsStillTheDeploymentsStoreIsRefusedAsMissingTheReleasesStore(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	seedBootstrapBundles(t, "store-v1", "writer-v1")
	m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
	installBootstrap(t, m, environment.TierProduction)
	storeScript, _ := bootstrapScriptNames(t, environment.TierProduction)
	const stale = "ocel-deployments-store"
	m.putBodies[stale], m.scriptSettings[stale], m.scriptSecrets[stale] = m.putBodies[storeScript], m.scriptSettings[storeScript], m.scriptSecrets[storeScript]
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

func TestADeployOntoABootstrapWithoutTheISRWriterIsRefused(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	seedBootstrapBundles(t, "store-v1", "writer-v1")
	m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
	installBootstrap(t, m, environment.TierProduction)
	_, writer := bootstrapScriptNames(t, environment.TierProduction)
	delete(m.putBodies, writer)
	delete(m.scriptSettings, writer)
	store := fakeStoreServer(t, "")

	_, err := m.provider(t).Reconcile(t.Context(), productionEntrySpec(store.URL), edge.StackState{})

	refused := refusedNotReady(t, err)
	for _, want := range []string{writer, "missing"} {
		if !strings.Contains(refused.Error(), want) {
			t.Errorf("refusal %q does not mention %q", refused.Error(), want)
		}
	}
}

func TestADeployOntoABootstrapWhoseWorkerBindingsDriftedIsRefused(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	seedBootstrapBundles(t, "store-v1", "writer-v1")
	m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
	installBootstrap(t, m, environment.TierProduction)
	storeScript, _ := bootstrapScriptNames(t, environment.TierProduction)
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
	seedBootstrapBundles(t, "store-v1", "writer-v1")
	m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
	installBootstrap(t, m, environment.TierProduction)
	store := fakeStoreServer(t, "")
	spec := productionEntrySpec(store.URL)

	if _, err := m.provider(t).Reconcile(t.Context(), spec, edge.StackState{}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if !slices.Contains(m.putScripts, spec.Program.Name) {
		t.Errorf("uploaded %v, want %q among them", m.putScripts, spec.Program.Name)
	}
}

func TestTheSharedPreviewEntryIsNotUploadedOntoABootstrapThatIsBehind(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	seedBootstrapBundles(t, "store-v1", "writer-v1")
	m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
	installBootstrap(t, m, environment.TierPreview)
	seedBootstrapBundles(t, "store-v1", "writer-v2")

	_, err := m.provider(t).ReconcilePreviewWildcard(t.Context(), previewWildcardSpec())

	refused := refusedNotReady(t, err)
	if !strings.Contains(refused.Error(), provider.BootstrapCommand(environment.TierPreview)) {
		t.Errorf("refusal %q does not name the preview bootstrap command", refused.Error())
	}
	if slices.Contains(m.putScripts, previewEntryScript) {
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
	seedBootstrapBundles(t, "store-v1", "writer-v1")

	cases := []struct {
		name    string
		tier    environment.Tier
		trigger func(t *testing.T, m *cfMock) error
		want    string
	}{
		{
			name: "a deploy with one worker behind",
			tier: environment.TierProduction,
			trigger: func(t *testing.T, m *cfMock) error {
				_, writer := bootstrapScriptNames(t, environment.TierProduction)
				delete(m.putBodies, writer)
				delete(m.scriptSettings, writer)
				store := fakeStoreServer(t, "")
				_, err := m.provider(t).Reconcile(t.Context(), productionEntrySpec(store.URL), edge.StackState{})
				return err
			},
			want: "and the entry worker this deploy uploads calls it, so serving it would fail every request it routes.\nRe-run `" +
				provider.BootstrapCommand(environment.TierProduction) + "` to install this build's, then deploy again",
		},
		{
			name: "a deploy with both workers missing",
			tier: environment.TierProduction,
			trigger: func(t *testing.T, m *cfMock) error {
				store, writer := bootstrapScriptNames(t, environment.TierProduction)
				for _, name := range []string{store, writer} {
					delete(m.putBodies, name)
					delete(m.scriptSettings, name)
				}
				srv := fakeStoreServer(t, "")
				_, err := m.provider(t).Reconcile(t.Context(), productionEntrySpec(srv.URL), edge.StackState{})
				return err
			},
			want: "and the entry worker this deploy uploads calls them, so",
		},
		{
			name: "the preview wildcard with one worker behind",
			tier: environment.TierPreview,
			trigger: func(t *testing.T, m *cfMock) error {
				seedBootstrapBundles(t, "store-v1", "writer-v2")
				_, err := m.provider(t).ReconcilePreviewWildcard(t.Context(), previewWildcardSpec())
				return err
			},
			want: "and the shared preview entry worker calls it, so serving it would fail every request it routes.\nRe-run `" +
				provider.BootstrapCommand(environment.TierPreview) + "` to install this build's, then run this again",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seedBootstrapBundles(t, "store-v1", "writer-v1")
			m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
			installBootstrap(t, m, tc.tier)

			refused := refusedNotReady(t, tc.trigger(t, m))

			if !strings.Contains(refused.Error(), tc.want) {
				t.Errorf("refusal %q does not contain %q", refused.Error(), tc.want)
			}
		})
	}
}

func refreshingCaller(certificateID string) bootstrapCaller {
	return bootstrapCaller{worker: "the entry worker this deploy uploads", retry: "deploy again", refreshes: true, certificateID: certificateID}
}

func TestADeployThatRefreshesThroughTheQueueIsRefusedUntilTheQueueIsBootstrapped(t *testing.T) {
	m := bootstrapMock(t, false)
	p := mutualTLSEdge(t, m)
	installBootstrap(t, m, environment.TierProduction)

	err := p.refuseBootstrapBehind(t.Context(), "acct", environment.TierProduction, refreshingCaller(""))

	refused := refusedNotReady(t, err)
	for _, want := range []string{`refresh queue "ocel-refresh" is missing`, `refresher worker "ocel-refresher" is missing`, "ocel bootstrap production"} {
		if !strings.Contains(refused.Message, want) {
			t.Errorf("refusal %q does not say %q", refused.Message, want)
		}
	}
}

func TestADeployThatRefreshesThroughTheQueueIsRefusedWhileItsRefresherIsBehind(t *testing.T) {
	m := bootstrapMock(t, false)
	p := mutualTLSEdge(t, m)
	if _, err := p.Bootstrap(t.Context(), environment.TierProduction); err != nil {
		t.Fatal(err)
	}
	if err := p.refuseBootstrapBehind(t.Context(), "acct", environment.TierProduction, refreshingCaller("mtls-1")); err != nil {
		t.Fatalf("refuseBootstrapBehind after a bootstrap = %v, want none", err)
	}
	prior := refresherBundle
	refresherBundle = []byte("export default {rebuilt:1}")
	t.Cleanup(func() { refresherBundle = prior })

	err := p.refuseBootstrapBehind(t.Context(), "acct", environment.TierProduction, refreshingCaller("mtls-1"))

	refused := refusedNotReady(t, err)
	if !strings.Contains(refused.Message, `refresher worker "ocel-refresher" is behind this build`) {
		t.Errorf("refusal %q does not name the refresher as behind", refused.Message)
	}
}

func TestADeployThatRefreshesThroughTheQueueIsRefusedWithoutAConsumer(t *testing.T) {
	m := bootstrapMock(t, false)
	p := mutualTLSEdge(t, m)
	if _, err := p.Bootstrap(t.Context(), environment.TierProduction); err != nil {
		t.Fatal(err)
	}
	m.queues[0]["consumers"] = []any{}

	err := p.refuseBootstrapBehind(t.Context(), "acct", environment.TierProduction, refreshingCaller("mtls-1"))

	refused := refusedNotReady(t, err)
	if !strings.Contains(refused.Message, `has no consumer that drains it into "ocel-refresher"`) {
		t.Errorf("refusal %q does not name the missing consumer", refused.Message)
	}
}

func TestADeployThatRefreshesElsewhereIsNotRefusedForTheRefreshQueue(t *testing.T) {
	m := bootstrapMock(t, false)
	p := mutualTLSEdge(t, m)
	installBootstrap(t, m, environment.TierProduction)

	err := p.refuseBootstrapBehind(t.Context(), "acct", environment.TierProduction, bootstrapCaller{worker: "the entry worker this deploy uploads", retry: "deploy again"})

	if err != nil {
		t.Errorf("refuseBootstrapBehind = %v, want a deploy that binds no refresh queue left alone", err)
	}
}

func TestADeployThatRefreshesThroughTheQueueReadsItsOneClientCertificateAndListsNoneInTheAccount(t *testing.T) {
	m := bootstrapMock(t, false)
	p := mutualTLSEdge(t, m)
	if _, err := p.Bootstrap(t.Context(), environment.TierProduction); err != nil {
		t.Fatal(err)
	}
	m.listedMTLSCertificates, m.gotMTLSCertificates = 0, nil

	if err := p.refuseBootstrapBehind(t.Context(), "acct", environment.TierProduction, refreshingCaller("mtls-1")); err != nil {
		t.Fatalf("refuseBootstrapBehind = %v, want none", err)
	}

	if m.listedMTLSCertificates != 0 || !slices.Equal(m.gotMTLSCertificates, []string{"mtls-1"}) {
		t.Errorf("listed the account's mTLS certificates %d times and got %v, want no listing and one get of the adopted certificate: a deploy's requests must not grow with the account", m.listedMTLSCertificates, m.gotMTLSCertificates)
	}
}
