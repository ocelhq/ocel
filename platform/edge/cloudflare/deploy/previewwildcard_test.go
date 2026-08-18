package cloudflare

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func uploadedMetadata(t *testing.T, m *cfMock, scriptName string) map[string]any {
	t.Helper()
	put, ok := m.putBodies[scriptName]
	if !ok {
		t.Fatalf("no script uploaded under %q; uploads = %v", scriptName, m.putScripts)
	}
	_, params, err := mime.ParseMediaType(put.contentType)
	if err != nil {
		t.Fatalf("parse content type: %v", err)
	}
	mr := multipart.NewReader(bytes.NewReader(put.content), params["boundary"])
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
	t.Fatal("no metadata part in the uploaded script")
	return nil
}

func previewWildcardSpec() edge.PreviewWildcardSpec {
	return edge.PreviewWildcardSpec{
		Version:    "v1",
		BaseDomain: "preview.app.com",
		GrammarMin: edge.PreviewGrammarMin,
		GrammarMax: edge.PreviewGrammarMax,
		Program:    &edge.ProgramSpec{Worker: edge.Worker{Main: mainModule()}},
	}
}

func TestReconcilePreviewWildcard(t *testing.T) {
	t.Setenv(envAccountID, "acct")

	t.Run("the shared worker claims the substrate wildcard and plants its record", func(t *testing.T) {
		m := &cfMock{
			zoneID:   "zone1",
			zoneName: "app.com",
			existingRoutes: []map[string]any{
				{"id": "project", "pattern": "pr-1-abc1234567.preview.app.com/*", "script": "ocel-shop-preview"},
			},
		}

		var warnings []string
		spec := previewWildcardSpec()
		spec.Warn = func(s string) { warnings = append(warnings, s) }
		if err := m.provider(t).ReconcilePreviewWildcard(t.Context(), spec); err != nil {
			t.Fatalf("ReconcilePreviewWildcard: %v", err)
		}

		if len(m.putScripts) != 1 || m.putScripts[0] != previewEntryScript {
			t.Errorf("uploaded scripts = %v, want [%s]", m.putScripts, previewEntryScript)
		}
		if len(m.createdRoutes) != 1 || m.createdRoutes[0]["pattern"] != "*.preview.app.com/*" {
			t.Errorf("created routes = %v, want the substrate wildcard *.preview.app.com/*", m.createdRoutes)
		}
		if m.createdRoutes[0]["script"] != previewEntryScript {
			t.Errorf("route script = %v, want %s", m.createdRoutes[0]["script"], previewEntryScript)
		}
		if len(m.createdRecords) != 1 || m.createdRecords[0]["name"] != "*.preview.app.com" || m.createdRecords[0]["proxied"] != true {
			t.Errorf("created records = %v, want a proxied placeholder for *.preview.app.com", m.createdRecords)
		}
		if len(m.deletedRoutes) != 0 || len(m.deletedRecords) != 0 {
			t.Errorf("deleted routes = %v and records = %v, want none: the shared entry never prunes a project's routes", m.deletedRoutes, m.deletedRecords)
		}
		if len(warnings) != 1 {
			t.Errorf("warnings = %v, want the Universal SSL coverage warning alone", warnings)
		}
	})

	t.Run("nothing is minted for a project", func(t *testing.T) {
		m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
		store := fakeStoreServer(t, "s3cr3t")

		if err := m.provider(t).ReconcilePreviewWildcard(t.Context(), previewWildcardSpec()); err != nil {
			t.Fatalf("ReconcilePreviewWildcard: %v", err)
		}

		meta := uploadedMetadata(t, m, previewEntryScript)
		for _, typ := range []string{"secret_text", "plain_text"} {
			if got := bindingsByType(meta, typ); len(got) != 0 {
				t.Errorf("%s bindings = %v, want none: the shared entry serves no single project", typ, got)
			}
		}

		if stamp, _, err := (&provider{}).getVersionStamp(t.Context(), store.URL, "acme-web", "s3cr3t"); err != nil || stamp != "" {
			t.Errorf("store version stamp = %q (err %v), want it untouched by a shared-entry reconcile", stamp, err)
		}
	})

	t.Run("the cache bucket and the isr writer are bound", func(t *testing.T) {
		m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
		spec := previewWildcardSpec()
		spec.Values = map[string]string{valueKeyCacheBucket: "ocel-edge-cache-preview"}
		spec.Program.ISRWriterScriptName = "ocel-isr-writer-preview"

		if err := m.provider(t).ReconcilePreviewWildcard(t.Context(), spec); err != nil {
			t.Fatalf("ReconcilePreviewWildcard: %v", err)
		}

		meta := uploadedMetadata(t, m, previewEntryScript)
		buckets := bindingsByType(meta, "r2_bucket")
		if len(buckets) != 1 || buckets[0]["name"] != cacheStoreBinding || buckets[0]["bucket_name"] != "ocel-edge-cache-preview" {
			t.Errorf("r2_bucket bindings = %v, want %s bound to the preview cache bucket: without it every static asset 404s", buckets, cacheStoreBinding)
		}
		services := bindingsByType(meta, "service")
		if len(services) != 1 || services[0]["name"] != genericISRWriterBinding || services[0]["service"] != "ocel-isr-writer-preview" {
			t.Errorf("service bindings = %v, want the ISR writer bound", services)
		}
		if got := bindingsByType(meta, "worker_loader"); len(got) != 1 {
			t.Errorf("worker_loader bindings = %v, want the loader the entry runs preview code through", got)
		}
	})

	t.Run("a spec without a base domain is an error", func(t *testing.T) {
		m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
		spec := previewWildcardSpec()
		spec.BaseDomain = ""

		if err := m.provider(t).ReconcilePreviewWildcard(t.Context(), spec); err == nil {
			t.Fatal("ReconcilePreviewWildcard without a base domain err = nil, want an error")
		}
	})

	t.Run("an unset account id is an error", func(t *testing.T) {
		t.Setenv(envAccountID, "")

		if err := (&provider{}).ReconcilePreviewWildcard(t.Context(), previewWildcardSpec()); err == nil {
			t.Fatal("ReconcilePreviewWildcard without an account id err = nil, want an error")
		}
	})
}

func TestDestroyPreviewWildcard(t *testing.T) {
	t.Setenv(envAccountID, "acct")

	t.Run("the wildcard route, its record and the script all go", func(t *testing.T) {
		m := &cfMock{
			zoneID:   "zone1",
			zoneName: "app.com",
			existingRoutes: []map[string]any{
				{"id": "entry", "pattern": "*.preview.app.com/*", "script": previewEntryScript},
				{"id": "project", "pattern": "pr-1-abc1234567.preview.app.com/*", "script": "ocel-shop-preview"},
			},
			existingRecords: []map[string]any{
				{"id": "wildcard", "name": "*.preview.app.com", "type": "AAAA", "content": "100::", "proxied": true},
				{"id": "projectrec", "name": "pr-1-abc1234567.preview.app.com", "type": "AAAA", "content": "100::", "proxied": true},
			},
			existingCustomDomains: []map[string]any{{"id": "cd1", "hostname": "preview.app.com", "service": previewEntryScript}},
		}

		if err := m.provider(t).DestroyPreviewWildcard(t.Context(), "preview.app.com"); err != nil {
			t.Fatalf("DestroyPreviewWildcard: %v", err)
		}

		assertSet(t, "deleted routes", m.deletedRoutes, []string{"entry"})
		assertSet(t, "deleted records", m.deletedRecords, []string{"wildcard"})
		assertSet(t, "deleted scripts", m.deletedScripts, []string{previewEntryScript})
		assertSet(t, "detached custom domains", m.deletedCustomDomains, []string{"cd1"})
	})

	t.Run("a wildcard another script holds is left standing", func(t *testing.T) {
		m := &cfMock{
			zoneID:   "zone1",
			zoneName: "app.com",
			existingRoutes: []map[string]any{
				{"id": "someone-elses", "pattern": "*.preview.app.com/*", "script": "ocel-shop-preview"},
			},
			existingRecords: []map[string]any{
				{"id": "wildcard", "name": "*.preview.app.com", "type": "AAAA", "content": "100::", "proxied": true},
			},
		}

		if err := m.provider(t).DestroyPreviewWildcard(t.Context(), "preview.app.com"); err != nil {
			t.Fatalf("DestroyPreviewWildcard: %v", err)
		}
		if len(m.deletedRoutes) != 0 {
			t.Errorf("deleted routes = %v, want none: the wildcard belongs to another worker", m.deletedRoutes)
		}
		if len(m.deletedRecords) != 0 {
			t.Errorf("deleted records = %v, want none: the surviving route needs its placeholder", m.deletedRecords)
		}
		assertSet(t, "deleted scripts", m.deletedScripts, []string{previewEntryScript})
	})

	t.Run("a route already gone still takes its record", func(t *testing.T) {
		m := &cfMock{
			zoneID:   "zone1",
			zoneName: "app.com",
			existingRecords: []map[string]any{
				{"id": "wildcard", "name": "*.preview.app.com", "type": "AAAA", "content": "100::", "proxied": true},
			},
		}

		if err := m.provider(t).DestroyPreviewWildcard(t.Context(), "preview.app.com"); err != nil {
			t.Fatalf("DestroyPreviewWildcard: %v", err)
		}
		assertSet(t, "deleted records", m.deletedRecords, []string{"wildcard"})
	})

	t.Run("an unset account id is an error", func(t *testing.T) {
		t.Setenv(envAccountID, "")

		if err := (&provider{}).DestroyPreviewWildcard(t.Context(), "preview.app.com"); err == nil {
			t.Fatal("DestroyPreviewWildcard without an account id err = nil, want an error")
		}
	})
}

func TestPruneStaleRoutesSparesThePreviewEntry(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		script string
		plan   routePlan
	}{
		{
			name:   "a stem that reaches the shared script never sweeps it",
			script: "ocel-preview",
			plan:   stemPlan("ocel-preview", "pr-1-abc1234567.preview.app.com"),
		},
		{
			name:   "the shared script does not sweep its own route",
			script: previewEntryScript,
			plan:   prunedPlan("pr-1-abc1234567.preview.app.com"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := &cfMock{
				zoneID:   "zone1",
				zoneName: "app.com",
				existingRoutes: []map[string]any{
					{"id": "entry", "pattern": "*.preview.app.com/*", "script": previewEntryScript},
				},
				existingRecords: []map[string]any{
					{"id": "wildcard", "name": "*.preview.app.com", "type": "AAAA", "content": "100::", "proxied": true},
				},
			}

			if err := m.provider(t).reconcileWorkerRoutes(t.Context(), upload{accountID: "acct", scriptName: tc.script}, tc.plan, nil); err != nil {
				t.Fatalf("reconcileWorkerRoutes: %v", err)
			}
			if len(m.deletedRoutes) != 0 {
				t.Errorf("deleted routes = %v, want none: a project deploy must never unhook the shared preview entry", m.deletedRoutes)
			}
			if len(m.deletedRecords) != 0 {
				t.Errorf("deleted records = %v, want none: the shared entry's placeholder outlives every project", m.deletedRecords)
			}
		})
	}
}
