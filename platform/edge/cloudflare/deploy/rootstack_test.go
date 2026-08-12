package cloudflare

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func doMetadataFromMultipart(t *testing.T, worker edge.Worker, do durableObjectWorker, deployedClasses []string) map[string]any {
	t.Helper()
	body, contentType, err := buildDurableObjectScriptMultipart(worker, do, deployedClasses)
	if err != nil {
		t.Fatalf("buildDurableObjectScriptMultipart: %v", err)
	}
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("parse content type: %v", err)
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		if part.FormName() != "metadata" {
			continue
		}
		data, _ := io.ReadAll(part)
		var meta map[string]any
		if err := json.Unmarshal(data, &meta); err != nil {
			t.Fatalf("unmarshal metadata: %v", err)
		}
		return meta
	}
	t.Fatal("no metadata part in multipart body")
	return nil
}

func testStoreWorker() edge.Worker {
	return edge.Worker{Main: mainModule()}
}

func doBindings(t *testing.T, meta map[string]any) map[string]string {
	t.Helper()
	bindings, _ := meta["bindings"].([]any)
	found := map[string]string{}
	for _, b := range bindings {
		m, ok := b.(map[string]any)
		if !ok || m["type"] != durableObjectBindingType {
			continue
		}
		name, _ := m["name"].(string)
		className, _ := m["class_name"].(string)
		found[name] = className
	}
	return found
}

func migrationSteps(t *testing.T, migrations map[string]any) [][]string {
	t.Helper()
	steps, ok := migrations["steps"].([]any)
	if !ok {
		t.Fatalf("migrations.steps = %v, want a list of steps", migrations["steps"])
	}
	out := [][]string{}
	for _, step := range steps {
		m, _ := step.(map[string]any)
		classes, _ := m["new_sqlite_classes"].([]any)
		names := []string{}
		for _, c := range classes {
			name, _ := c.(string)
			names = append(names, name)
		}
		out = append(out, names)
	}
	return out
}

func TestBuildDurableObjectScriptMultipart(t *testing.T) {
	t.Run("every declared class gets its binding", func(t *testing.T) {
		for _, do := range []durableObjectWorker{deploymentsStoreWorker, isrWriterWorker} {
			found := doBindings(t, doMetadataFromMultipart(t, testStoreWorker(), do, nil))
			if len(found) != len(do.classes) {
				t.Errorf("bound %d Durable Object classes, want %d: %v", len(found), len(do.classes), found)
			}
			for _, class := range do.classes {
				if found[class.binding] != class.className {
					t.Errorf("binding %s = %q, want %q", class.binding, found[class.binding], class.className)
				}
			}
		}
	})

	t.Run("a fresh bootstrap declares the whole migration log", func(t *testing.T) {
		meta := doMetadataFromMultipart(t, testStoreWorker(), isrWriterWorker, nil)
		migrations, ok := meta["migrations"].(map[string]any)
		if !ok {
			t.Fatalf("expected a migrations object on a fresh bootstrap, got %v", meta["migrations"])
		}
		if _, present := migrations["old_tag"]; present {
			t.Errorf("migrations.old_tag = %v, want none: there is no deployed tag to verify against", migrations["old_tag"])
		}
		if migrations["new_tag"] != "v2" {
			t.Errorf("migrations.new_tag = %v, want v2", migrations["new_tag"])
		}
		if steps := migrationSteps(t, migrations); !reflect.DeepEqual(steps, [][]string{{"IsrDeploy"}, {"IsrSnapshot"}}) {
			t.Errorf("migrations.steps = %v, want the whole log", steps)
		}
	})

	t.Run("a bootstrapped account declares only what it lacks", func(t *testing.T) {
		meta := doMetadataFromMultipart(t, testStoreWorker(), isrWriterWorker, []string{"IsrDeploy"})
		migrations, ok := meta["migrations"].(map[string]any)
		if !ok {
			t.Fatalf("expected a migrations object for a script behind the log, got %v", meta["migrations"])
		}
		if _, present := migrations["old_tag"]; present {
			t.Errorf("migrations.old_tag = %v, want none: the API reports no tag to verify against", migrations["old_tag"])
		}
		if migrations["new_tag"] != "v2" {
			t.Errorf("migrations.new_tag = %v, want v2", migrations["new_tag"])
		}
		if steps := migrationSteps(t, migrations); !reflect.DeepEqual(steps, [][]string{{"IsrSnapshot"}}) {
			t.Errorf("migrations.steps = %v, want only the class the script lacks", steps)
		}
	})

	t.Run("an up-to-date script declares no migration at all", func(t *testing.T) {
		for _, do := range []durableObjectWorker{deploymentsStoreWorker, isrWriterWorker} {
			var deployed []string
			for _, step := range do.migrations {
				deployed = append(deployed, step.sqliteClasses...)
			}
			meta := doMetadataFromMultipart(t, testStoreWorker(), do, deployed)
			if _, present := meta["migrations"]; present {
				t.Errorf("expected no migrations on an up-to-date script, got %v", meta["migrations"])
			}
		}
	})

	t.Run("a deployed class this build's log does not create is refused", func(t *testing.T) {
		if _, _, err := buildDurableObjectScriptMultipart(testStoreWorker(), isrWriterWorker, []string{"IsrDeploy", "IsrFuture"}); err == nil {
			t.Error("buildDurableObjectScriptMultipart(deployed IsrFuture) = nil error, want a refusal")
		}
	})

	t.Run("the account-level bucket binding rides along", func(t *testing.T) {
		worker := testStoreWorker()
		worker.ObjectStore = edge.ObjectStore{Binding: cacheStoreBinding, Bucket: cacheStoreName(edge.ClassProduction)}

		meta := doMetadataFromMultipart(t, worker, isrWriterWorker, nil)
		buckets := bindingsByType(meta, "r2_bucket")
		if len(buckets) != 1 {
			t.Fatalf("r2_bucket bindings = %v, want 1", buckets)
		}
		if buckets[0]["name"] != cacheStoreBinding || buckets[0]["bucket_name"] != cacheStoreName(edge.ClassProduction) {
			t.Errorf("r2 binding = %v, want name %s bucket %s", buckets[0], cacheStoreBinding, cacheStoreName(edge.ClassProduction))
		}
	})

	t.Run("switching observability off reaches the account-level workers too", func(t *testing.T) {
		t.Setenv(envObservability, "off")

		meta := doMetadataFromMultipart(t, testStoreWorker(), deploymentsStoreWorker, nil)
		obs, ok := meta["observability"].(map[string]any)
		if !ok {
			t.Fatalf("metadata has no observability object: %v", meta["observability"])
		}
		if obs["enabled"] != false {
			t.Errorf("observability.enabled = %v, want false", obs["enabled"])
		}
	})
}

func TestAccountScriptNameFor(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name          string
		nameFor       func(edge.Class) (string, error)
		prod, preview string
	}{
		{"the deployments store", storeScriptNameFor, sharedStoreScriptName, previewStoreScriptName},
		{"the isr writer", isrWriterScriptNameFor, isrWriterScriptName, previewISRWriterScriptName},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			t.Run("production and preview name their own script", func(t *testing.T) {
				t.Parallel()

				prod, err := tc.nameFor(edge.ClassProduction)
				if err != nil {
					t.Fatalf("production: %v", err)
				}
				preview, err := tc.nameFor(edge.ClassPreview)
				if err != nil {
					t.Fatalf("preview: %v", err)
				}
				if prod != tc.prod || preview != tc.preview {
					t.Errorf("script names = (%q, %q), want (%q, %q)", prod, preview, tc.prod, tc.preview)
				}
				if prod == preview {
					t.Error("production and preview scripts must differ so their DO namespaces do not collide")
				}
			})

			t.Run("an unknown substrate class is an error", func(t *testing.T) {
				t.Parallel()

				if _, err := tc.nameFor(edge.Class("nonsense")); err == nil {
					t.Error("nameFor(unknown class) = nil error, want an error")
				}
			})
		})
	}

	t.Run("the isr writer and the deployments store never share a name", func(t *testing.T) {
		t.Parallel()

		if isrWriterScriptName == sharedStoreScriptName || previewISRWriterScriptName == previewStoreScriptName {
			t.Error("the isr-writer and deployments-store scripts must be distinct")
		}
	})
}

func testSpec(endpoint, version string) edge.RootStackSpec {
	return edge.RootStackSpec{
		Slug:          "acme-web",
		StoreEndpoint: endpoint,
		BootstrapCred: storeBootstrapCred,
		Version:       version,
	}
}

func previewSpec(endpoint, version string) edge.RootStackSpec {
	spec := testSpec(endpoint, version)
	spec.GenericName = "ocel-preview"
	spec.Generic = testStoreWorker()
	spec.Domains = []string{"*.preview.app.com"}
	spec.PruneRoutes = true
	return spec
}

func pruneOnlySpec(endpoint, version string) edge.RootStackSpec {
	spec := previewSpec(endpoint, version)
	spec.Domains = nil
	spec.PruneOnly = true
	spec.PruneWorkerStem = "ocel-preview"
	return spec
}

func specStampFor(t *testing.T, spec edge.RootStackSpec) string {
	t.Helper()
	stamp, err := specStamp(spec, genericWorker(spec, spec.Slug))
	if err != nil {
		t.Fatalf("specStamp: %v", err)
	}
	return stamp
}

func putStampSet(t *testing.T, p *provider, endpoint, secret string, set stampSet) {
	t.Helper()
	encoded, err := set.encode()
	if err != nil {
		t.Fatalf("encode stamp set: %v", err)
	}
	if err := p.putVersionStamp(t.Context(), endpoint, "acme-web", secret, encoded); err != nil {
		t.Fatalf("putVersionStamp: %v", err)
	}
}

func readStampSet(t *testing.T, p *provider, endpoint, secret string) stampSet {
	t.Helper()
	raw, _, err := p.getVersionStamp(t.Context(), endpoint, "acme-web", secret)
	if err != nil {
		t.Fatalf("getVersionStamp: %v", err)
	}
	return decodeStampSet(raw)
}

func previewZoneMock() *cfMock {
	return &cfMock{
		zoneID:   "zone1",
		zoneName: "app.com",
		existingRecords: []map[string]any{
			{"id": "wildcard", "name": "*.preview.app.com", "type": "AAAA", "content": "100::", "proxied": true},
		},
	}
}

func TestReconcileRootStack(t *testing.T) {
	t.Setenv(envAccountID, "acct")

	t.Run("a root stack already at the spec stamp still reconciles its route", func(t *testing.T) {
		store := fakeStoreServer(t, "s3cr3t")
		m := previewZoneMock()
		p := m.provider(t)
		spec := previewSpec(store.URL, "v2")
		putStampSet(t, p, store.URL, "s3cr3t", stampSet{spec.GenericName: specStampFor(t, spec)})

		prior := testState(store.URL, "s3cr3t")
		state, err := p.ReconcileRootStack(t.Context(), spec, prior)
		if err != nil {
			t.Fatalf("ReconcileRootStack: %v", err)
		}

		if !reflect.DeepEqual(state, prior) {
			t.Errorf("state = %v, want prior handed back unchanged (%v)", state, prior)
		}
		if len(m.createdRoutes) != 1 || m.createdRoutes[0]["pattern"] != "*.preview.app.com/*" {
			t.Errorf("created routes = %v, want the project's wildcard route", m.createdRoutes)
		}
		if len(m.putScripts) != 0 {
			t.Errorf("uploaded scripts = %v, want none: the root stack already carries this spec's stamp", m.putScripts)
		}
	})

	t.Run("a preview prunes every route but its own wildcard", func(t *testing.T) {
		store := fakeStoreServer(t, "s3cr3t")
		m := previewZoneMock()
		m.existingRoutes = []map[string]any{
			{"id": "stale", "pattern": "pr-1-abc1234567.preview.app.com/*", "script": "ocel-preview"},
			{"id": "other", "pattern": "pr-2-abc1234567.preview.app.com/*", "script": "someone-else"},
		}

		if _, err := m.provider(t).ReconcileRootStack(t.Context(), previewSpec(store.URL, "v2"), testState(store.URL, "s3cr3t")); err != nil {
			t.Fatalf("ReconcileRootStack: %v", err)
		}

		if len(m.deletedRoutes) != 1 || m.deletedRoutes[0] != "stale" {
			t.Errorf("deleted routes = %v, want only the stale pointer-exact route", m.deletedRoutes)
		}
	})

	t.Run("a preview plants its own wildcard record", func(t *testing.T) {
		store := fakeStoreServer(t, "s3cr3t")
		m := previewZoneMock()
		m.existingRecords = nil

		if _, err := m.provider(t).ReconcileRootStack(t.Context(), previewSpec(store.URL, "v2"), testState(store.URL, "s3cr3t")); err != nil {
			t.Fatalf("ReconcileRootStack: %v", err)
		}

		if len(m.createdRecords) != 1 || m.createdRecords[0]["name"] != "*.preview.app.com" {
			t.Errorf("created records = %v, want the proxied placeholder for *.preview.app.com", m.createdRecords)
		}
	})

	t.Run("a root stack behind the spec stamp uploads and stamps", func(t *testing.T) {
		store := fakeStoreServer(t, "s3cr3t")
		m := previewZoneMock()
		p := m.provider(t)
		spec := previewSpec(store.URL, "v2")

		state, err := p.ReconcileRootStack(t.Context(), spec, testState(store.URL, "s3cr3t"))
		if err != nil {
			t.Fatalf("ReconcileRootStack: %v", err)
		}

		if len(m.putScripts) != 1 || m.putScripts[0] != "ocel-preview" {
			t.Errorf("uploaded scripts = %v, want [ocel-preview]", m.putScripts)
		}
		if len(m.createdRoutes) != 1 {
			t.Errorf("created routes = %v, want the project's wildcard route", m.createdRoutes)
		}
		stamps := readStampSet(t, p, store.URL, state[edge.RootStackKeySecret])
		if want := specStampFor(t, spec); stamps[spec.GenericName] != want {
			t.Errorf("version stamps = %v, want %q under %q", stamps, want, spec.GenericName)
		}
	})

	t.Run("an opted-in second reconcile of the same spec touches Cloudflare not at all", func(t *testing.T) {
		t.Setenv(envSkipEdgeReconcile, "1")

		store := fakeStoreServer(t, "s3cr3t")
		m := previewZoneMock()
		p := m.provider(t)
		spec := previewSpec(store.URL, "v2")

		state, err := p.ReconcileRootStack(t.Context(), spec, testState(store.URL, "s3cr3t"))
		if err != nil {
			t.Fatalf("ReconcileRootStack: %v", err)
		}

		spent := m.requests
		if _, err := p.ReconcileRootStack(t.Context(), spec, state); err != nil {
			t.Fatalf("second ReconcileRootStack: %v", err)
		}
		if m.requests != spent {
			t.Errorf("Cloudflare requests = %d, want the %d the first reconcile already spent", m.requests, spent)
		}
	})

	t.Run("a second reconcile of the same spec still reconciles routes by default", func(t *testing.T) {
		store := fakeStoreServer(t, "s3cr3t")
		m := previewZoneMock()
		p := m.provider(t)
		spec := previewSpec(store.URL, "v2")

		state, err := p.ReconcileRootStack(t.Context(), spec, testState(store.URL, "s3cr3t"))
		if err != nil {
			t.Fatalf("ReconcileRootStack: %v", err)
		}

		if _, err := p.ReconcileRootStack(t.Context(), spec, state); err != nil {
			t.Fatalf("second ReconcileRootStack: %v", err)
		}
		if m.routeLists != 2 {
			t.Errorf("route lists = %d, want 2: every deploy repairs its routes unless %s says otherwise", m.routeLists, envSkipEdgeReconcile)
		}
		if len(m.putScripts) != 1 {
			t.Errorf("uploaded scripts = %v, want the first reconcile's alone", m.putScripts)
		}
	})

	t.Run("a preview whose app list grew re-uploads the shared worker", func(t *testing.T) {
		t.Setenv(envSkipEdgeReconcile, "1")

		store := fakeStoreServer(t, "s3cr3t")
		m := previewZoneMock()
		p := m.provider(t)

		spec := previewSpec(store.URL, "v2")
		spec.Generic = withVar(spec.Generic, "OCEL_PREVIEW_APPS", "web")
		state, err := p.ReconcileRootStack(t.Context(), spec, testState(store.URL, "s3cr3t"))
		if err != nil {
			t.Fatalf("ReconcileRootStack: %v", err)
		}

		grown := previewSpec(store.URL, "v2")
		grown.Generic = withVar(grown.Generic, "OCEL_PREVIEW_APPS", "web,api")
		if _, err := p.ReconcileRootStack(t.Context(), grown, state); err != nil {
			t.Fatalf("ReconcileRootStack after the app list grew: %v", err)
		}

		if len(m.putScripts) != 2 {
			t.Errorf("uploaded scripts = %v, want the shared worker uploaded again for the wider app list", m.putScripts)
		}
	})

	t.Run("two apps on one slug each read back their own stamp", func(t *testing.T) {
		t.Setenv(envSkipEdgeReconcile, "1")

		store := fakeStoreServer(t, "s3cr3t")
		m := previewZoneMock()
		p := m.provider(t)

		appSpec := func(name string) edge.RootStackSpec {
			spec := testSpec(store.URL, "v2")
			spec.GenericName = "ocel-" + name
			spec.Generic = withVar(testStoreWorker(), "OCEL_APP", name)
			spec.Domains = []string{name + ".app.com"}
			return spec
		}
		specs := []edge.RootStackSpec{appSpec("web"), appSpec("api")}

		state := testState(store.URL, "s3cr3t")
		for pass := range 2 {
			for _, spec := range specs {
				next, err := p.ReconcileRootStack(t.Context(), spec, state)
				if err != nil {
					t.Fatalf("pass %d, %s: %v", pass, spec.GenericName, err)
				}
				state = next
			}
		}

		if len(m.putScripts) != 2 {
			t.Errorf("uploaded scripts = %v, want each app's worker uploaded once: on the second pass every app must find its own stamp, not the app reconciled after it", m.putScripts)
		}
		stamps := readStampSet(t, p, store.URL, state[edge.RootStackKeySecret])
		for _, spec := range specs {
			if want := specStampFor(t, spec); stamps[spec.GenericName] != want {
				t.Errorf("version stamps = %v, want %q under %q", stamps, want, spec.GenericName)
			}
		}
	})

	t.Run("the identity the instance reports is the one persisted", func(t *testing.T) {
		store := fakeStoreServer(t, "s3cr3t")
		m := previewZoneMock()

		state, err := m.provider(t).ReconcileRootStack(t.Context(), previewSpec(store.URL, "v3"), nil)
		if err != nil {
			t.Fatalf("ReconcileRootStack: %v", err)
		}
		if state[edge.RootStackKeySecret] != "s3cr3t" {
			t.Errorf("persisted secret = %q, want the instance's own", state[edge.RootStackKeySecret])
		}
		if state[edge.RootStackKeyOwnerToken] != storeOwnerToken {
			t.Errorf("persisted owner token = %q, want the instance's own", state[edge.RootStackKeyOwnerToken])
		}
	})

	t.Run("a prune-only spec exposes no worker yet still records the instance and prunes", func(t *testing.T) {
		store := fakeStoreServer(t, "s3cr3t")
		m := previewZoneMock()
		m.existingRoutes = []map[string]any{
			{"id": "stale", "pattern": "pr-1-abc1234567.preview.app.com/*", "script": "ocel-preview"},
			{"id": "sibling", "pattern": "pr-2-abc1234567.preview.app.com/*", "script": "ocel-preview--web"},
			{"id": "entry", "pattern": "*.preview.ocel.app/*", "script": edge.SharedPreviewEntryScript},
			{"id": "other", "pattern": "pr-3-abc1234567.preview.app.com/*", "script": "someone-else"},
		}
		p := m.provider(t)
		spec := pruneOnlySpec(store.URL, "v2")

		state, err := p.ReconcileRootStack(t.Context(), spec, testState(store.URL, "s3cr3t"))
		if err != nil {
			t.Fatalf("ReconcileRootStack: %v", err)
		}

		if len(m.putScripts) != 0 {
			t.Errorf("uploaded scripts = %v, want none: the shared entry worker serves these hostnames", m.putScripts)
		}
		if len(m.subdomainCalls) != 0 {
			t.Errorf("subdomain calls = %v, want none: a prune-only worker is never exposed on workers.dev", m.subdomainCalls)
		}
		if !slices.Equal(m.deletedScripts, []string{spec.GenericName}) {
			t.Errorf("deleted scripts = %v, want the retired per-project preview worker alone", m.deletedScripts)
		}
		if len(m.createdRoutes) != 0 {
			t.Errorf("created routes = %v, want none", m.createdRoutes)
		}
		assertSet(t, "deleted routes", m.deletedRoutes, []string{"stale", "sibling"})
		if state[edge.RootStackKeySecret] != "s3cr3t" || state[edge.RootStackKeyEndpoint] != store.URL {
			t.Errorf("state = %v, want the store instance's identity", state)
		}
		stamps := readStampSet(t, p, store.URL, state[edge.RootStackKeySecret])
		if want := specStampFor(t, spec); stamps[spec.GenericName] != want {
			t.Errorf("version stamps = %v, want %q under %q", stamps, want, spec.GenericName)
		}
	})

	t.Run("a prune-only spec refuses to target the shared entry worker", func(t *testing.T) {
		store := fakeStoreServer(t, "s3cr3t")
		spec := pruneOnlySpec(store.URL, "v2")
		spec.GenericName = edge.SharedPreviewEntryScript

		if _, err := previewZoneMock().provider(t).ReconcileRootStack(t.Context(), spec, testState(store.URL, "s3cr3t")); err == nil {
			t.Fatal("ReconcileRootStack err = nil, want a refusal to prune the shared entry worker")
		}
	})
}

func TestListDeployedWorkers(t *testing.T) {
	t.Setenv(envAccountID, "acct")

	for _, tc := range []struct {
		name    string
		scripts []string
		want    []string
	}{
		{
			name: "only the stem's own family comes back",
			scripts: []string{
				"ocel-shop-preview",
				"ocel-shop-preview-web",
				"ocel-shop-previewer",
				"ocel-shopfoo-preview",
				"ocel-shop-prod-web",
				"ocel-other-preview",
			},
			want: []string{"ocel-shop-preview", "ocel-shop-preview-web"},
		},
		{
			name:    "nothing under the stem is not an error",
			scripts: []string{"ocel-other-preview"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &cfMock{zoneID: "zone1", zoneName: "app.com", existingScripts: tc.scripts}

			names, err := m.provider(t).ListDeployedWorkers(t.Context(), "ocel-shop-preview")
			if err != nil {
				t.Fatalf("ListDeployedWorkers: %v", err)
			}
			if !slices.Equal(names, tc.want) {
				t.Errorf("names = %v, want %v", names, tc.want)
			}
		})
	}
}

func TestDestroyRootStack(t *testing.T) {
	t.Run("an empty list is a no-op", func(t *testing.T) {
		t.Setenv(envAccountID, "acct-1")

		if err := (&provider{}).DestroyRootStack(t.Context(), nil); err != nil {
			t.Fatalf("DestroyRootStack(nil) err = %v, want nil", err)
		}
	})

	t.Run("an unset account id is an error", func(t *testing.T) {
		t.Setenv(envAccountID, "")

		if err := (&provider{}).DestroyRootStack(t.Context(), []string{"ocel-proj-prod-web"}); err == nil {
			t.Fatal("DestroyRootStack without an account id err = nil, want an error")
		}
	})
}

func TestEnsureInstance(t *testing.T) {
	t.Parallel()

	t.Run("an instance wiped by a failed teardown is reseeded", func(t *testing.T) {
		t.Parallel()

		srv := fakeStoreServer(t, "s3cr3t")
		p := &provider{}
		state := testState(srv.URL, "s3cr3t")

		if err := p.DestroyInstance(t.Context(), state); err != nil {
			t.Fatalf("DestroyInstance: %v", err)
		}

		id, stamps, err := p.ensureInstance(t.Context(), testSpec(srv.URL, "v2"), state)
		if err != nil {
			t.Fatalf("ensureInstance after a wipe: %v", err)
		}
		if len(stamps) != 0 {
			t.Errorf("stamps = %v, want none: a wiped instance carries no version stamp", stamps)
		}
		if id.secret == "" || id.ownerToken == "" {
			t.Fatalf("identity = %+v, want the freshly seeded pair the wiped instance now carries", id)
		}
		if err := p.putVersionStamp(t.Context(), srv.URL, "acme-web", id.secret, "v2"); err != nil {
			t.Fatalf("the re-seeded instance still rejects the project: %v", err)
		}
	})

	t.Run("the identity an already-seeded instance reports is adopted", func(t *testing.T) {
		t.Parallel()

		srv := fakeStoreServer(t, "s3cr3t")
		p := &provider{}

		id, stamps, err := p.ensureInstance(t.Context(), testSpec(srv.URL, "v2"), nil)
		if err != nil {
			t.Fatalf("ensureInstance: %v", err)
		}
		if len(stamps) != 0 {
			t.Errorf("stamps = %v, want none: this reconcile read no version stamp of its own", stamps)
		}
		if id.secret != "s3cr3t" || id.ownerToken != storeOwnerToken {
			t.Fatalf("identity = %+v, want the pair the instance already carries", id)
		}
		if err := p.putVersionStamp(t.Context(), srv.URL, "acme-web", id.secret, "v2"); err != nil {
			t.Fatalf("the adopted secret does not authenticate: %v", err)
		}
	})

	t.Run("a stamp already matching this spec skips the reconcile", func(t *testing.T) {
		t.Parallel()

		srv := fakeStoreServer(t, "s3cr3t")
		p := &provider{}
		putStampSet(t, p, srv.URL, "s3cr3t", stampSet{"": "stamp-v2"})
		id, stamps, err := p.ensureInstance(t.Context(), testSpec(srv.URL, "v2"), testState(srv.URL, "s3cr3t"))
		if err != nil {
			t.Fatalf("ensureInstance: %v", err)
		}
		if stamps[""] != "stamp-v2" {
			t.Errorf("stamps = %v, want the stamp the root stack already carries", stamps)
		}
		if id.secret != "s3cr3t" {
			t.Errorf("secret = %q, want the one already in state", id.secret)
		}
	})

	t.Run("a first reconcile seeds two distinct credentials", func(t *testing.T) {
		t.Parallel()

		srv := fakeStoreServer(t, "")
		p := &provider{}

		id, stamps, err := p.ensureInstance(t.Context(), testSpec(srv.URL, "v1"), nil)
		if err != nil {
			t.Fatalf("ensureInstance with no prior state: %v", err)
		}
		if len(stamps) != 0 {
			t.Errorf("stamps = %v, want none for a project with no root stack yet", stamps)
		}
		if id.secret == "" || id.ownerToken == "" || id.secret == id.ownerToken {
			t.Fatalf("minted identity = %+v, want two distinct non-empty credentials", id)
		}
		if err := p.putVersionStamp(t.Context(), srv.URL, "acme-web", id.secret, "v1"); err != nil {
			t.Fatalf("the seeded instance rejects the minted secret: %v", err)
		}
	})

	t.Run("a store failure is never mistaken for a lost secret", func(t *testing.T) {
		t.Parallel()

		var initialized int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				initialized++
			}
			w.Header().Set("Retry-After-Ms", "1")
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)

		if _, _, err := (&provider{}).ensureInstance(t.Context(), testSpec(srv.URL, "v2"), testState(srv.URL, "s3cr3t")); err == nil {
			t.Fatal("ensureInstance err = nil, want the store failure surfaced")
		}
		if initialized != 0 {
			t.Errorf("initialize calls = %d, want 0: a 500 is not a lost secret", initialized)
		}
	})
}

func TestMintSecret(t *testing.T) {
	t.Parallel()

	t.Run("every mint is non-empty and unlike the last", func(t *testing.T) {
		t.Parallel()

		a, err := mintSecret()
		if err != nil {
			t.Fatalf("mintSecret: %v", err)
		}
		b, err := mintSecret()
		if err != nil {
			t.Fatalf("mintSecret: %v", err)
		}
		if a == "" || b == "" {
			t.Fatal("expected non-empty secrets")
		}
		if a == b {
			t.Fatal("two mints produced the same secret")
		}
	})
}

func TestWorkerDecoration(t *testing.T) {
	t.Parallel()

	t.Run("withService leaves the caller's worker alone", func(t *testing.T) {
		t.Parallel()

		worker := edge.Worker{Services: map[string]string{"EXISTING": "x"}}
		out := withService(worker, "DEPLOYMENTS", "ocel-proj-store")

		if _, ok := worker.Services["DEPLOYMENTS"]; ok {
			t.Error("withService mutated the caller's Worker.Services map")
		}
		if out.Services["DEPLOYMENTS"] != "ocel-proj-store" || out.Services["EXISTING"] != "x" {
			t.Errorf("out.Services = %v", out.Services)
		}
	})

	t.Run("withSecret leaves the caller's worker alone", func(t *testing.T) {
		t.Parallel()

		worker := edge.Worker{Secrets: map[string]string{"EXISTING": "1"}}
		out := withSecret(worker, "WRITE_SECRET", "s")

		if _, ok := worker.Secrets["WRITE_SECRET"]; ok {
			t.Error("withSecret mutated the caller's Worker.Secrets map")
		}
		if out.Secrets["WRITE_SECRET"] != "s" || out.Secrets["EXISTING"] != "1" {
			t.Errorf("out.Secrets = %v", out.Secrets)
		}
	})

	t.Run("the generic worker binds every account worker it reaches", func(t *testing.T) {
		t.Parallel()

		spec := edge.RootStackSpec{
			Generic:             testStoreWorker(),
			StoreScriptName:     "ocel-deployments-store",
			ISRWriterScriptName: "ocel-isr-writer",
		}

		worker := genericWorker(spec, "acme-web")

		if worker.Services[genericStoreBinding] != "ocel-deployments-store" {
			t.Errorf("Services[%s] = %q", genericStoreBinding, worker.Services[genericStoreBinding])
		}
		if worker.Services[genericISRWriterBinding] != "ocel-isr-writer" {
			t.Errorf("Services[%s] = %q", genericISRWriterBinding, worker.Services[genericISRWriterBinding])
		}
		if worker.Vars[genericSlugBinding] != "acme-web" {
			t.Errorf("Vars[%s] = %q", genericSlugBinding, worker.Vars[genericSlugBinding])
		}
	})

	t.Run("the isr writer is left unbound when the substrate offers none", func(t *testing.T) {
		t.Parallel()

		spec := edge.RootStackSpec{Generic: testStoreWorker(), StoreScriptName: "ocel-deployments-store"}

		worker := genericWorker(spec, "acme-web")

		if _, bound := worker.Services[genericISRWriterBinding]; bound {
			t.Errorf("Services = %v, want no %s binding", worker.Services, genericISRWriterBinding)
		}
	})
}
