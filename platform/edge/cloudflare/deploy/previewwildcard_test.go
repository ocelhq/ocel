package cloudflare

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
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
		Program:    previewProgrammed(),
	}
}

func TestReconcilePreviewWildcard(t *testing.T) {
	t.Setenv(envAccountID, "acct")

	t.Run("the shared worker claims the bootstrap wildcard and plants nothing", func(t *testing.T) {
		m := &cfMock{
			bootstrapInstalled: true,
			zoneID:             "zone1",
			zoneName:           "app.com",
			existingRoutes: []map[string]any{
				{"id": "project", "pattern": "pr-1-abc1234567.preview.app.com/*", "script": "ocel-shop-preview"},
			},
		}

		var warnings []string
		spec := previewWildcardSpec()
		spec.Warn = func(s string) { warnings = append(warnings, s) }
		if _, err := m.provider(t).ReconcilePreviewWildcard(t.Context(), spec); err != nil {
			t.Fatalf("ReconcilePreviewWildcard: %v", err)
		}

		if len(m.putScripts) != 1 || m.putScripts[0] != previewEntryScriptName {
			t.Errorf("uploaded scripts = %v, want [%s]", m.putScripts, previewEntryScriptName)
		}
		if len(m.createdRoutes) != 1 || m.createdRoutes[0]["pattern"] != "*.preview.app.com/*" {
			t.Errorf("created routes = %v, want the bootstrap wildcard *.preview.app.com/*", m.createdRoutes)
		}
		if m.createdRoutes[0]["script"] != previewEntryScriptName {
			t.Errorf("route script = %v, want %s", m.createdRoutes[0]["script"], previewEntryScriptName)
		}
		if len(m.createdRecords) != 0 {
			t.Errorf("created records = %v, want none: the wildcard's record is the DNS axis's to write", m.createdRecords)
		}
		if len(m.deletedRoutes) != 0 || len(m.deletedRecords) != 0 {
			t.Errorf("deleted routes = %v and records = %v, want none: the shared entry never prunes a project's routes", m.deletedRoutes, m.deletedRecords)
		}
		if len(warnings) != 1 {
			t.Errorf("warnings = %v, want the Universal SSL coverage warning alone", warnings)
		}
	})

	t.Run("nothing is minted for a project", func(t *testing.T) {
		m := &cfMock{zoneID: "zone1", zoneName: "app.com", bootstrapInstalled: true}
		store := fakeStoreServer(t, "s3cr3t")

		if _, err := m.provider(t).ReconcilePreviewWildcard(t.Context(), previewWildcardSpec()); err != nil {
			t.Fatalf("ReconcilePreviewWildcard: %v", err)
		}

		meta := uploadedMetadata(t, m, previewEntryScriptName)
		for _, typ := range []string{"secret_text", "plain_text"} {
			for _, binding := range bindingsByType(meta, typ) {
				if name := binding["name"]; name == genericSlugBinding || name == envelopeKeyBinding {
					t.Errorf("%s binding %v names a project's own value, want none: the shared entry serves no single project", typ, binding)
				}
			}
		}

		if stamp, _, err := (&cloudflare{}).getVersionStamp(t.Context(), store.URL, "acme-web", "s3cr3t"); err != nil || stamp != "" {
			t.Errorf("store version stamp = %q (err %v), want it untouched by a shared-entry reconcile", stamp, err)
		}
	})

	t.Run("the code loader is bound, and no bucket is", func(t *testing.T) {
		m := &cfMock{zoneID: "zone1", zoneName: "app.com", bootstrapInstalled: true}

		if _, err := m.provider(t).ReconcilePreviewWildcard(t.Context(), previewWildcardSpec()); err != nil {
			t.Fatalf("ReconcilePreviewWildcard: %v", err)
		}

		meta := uploadedMetadata(t, m, previewEntryScriptName)
		if got := bindingsByType(meta, "r2_bucket"); len(got) != 0 {
			t.Errorf("r2_bucket bindings = %v, want none: the entry reads objects through Serve", got)
		}
		if got := bindingsByType(meta, "worker_loader"); len(got) != 1 {
			t.Errorf("worker_loader bindings = %v, want the loader the entry runs preview code through", got)
		}
	})

	t.Run("the shared worker answers purges on workers.dev, where each project's origin sends them", func(t *testing.T) {
		m := &cfMock{zoneID: "zone1", zoneName: "app.com", bootstrapInstalled: true}

		if _, err := m.provider(t).ReconcilePreviewWildcard(t.Context(), previewWildcardSpec()); err != nil {
			t.Fatalf("ReconcilePreviewWildcard: %v", err)
		}

		if !m.subdomainOn[previewEntryScriptName] {
			t.Errorf("workers.dev subdomain of %s = off, want on: a preview's cached prerenders are purged through it", previewEntryScriptName)
		}
	})

	t.Run("a spec without a base domain is an error", func(t *testing.T) {
		m := &cfMock{zoneID: "zone1", zoneName: "app.com", bootstrapInstalled: true}
		spec := previewWildcardSpec()
		spec.BaseDomain = ""

		if _, err := m.provider(t).ReconcilePreviewWildcard(t.Context(), spec); err == nil {
			t.Fatal("ReconcilePreviewWildcard without a base domain err = nil, want an error")
		}
	})

	t.Run("an unset account id is an error", func(t *testing.T) {
		t.Setenv(envAccountID, "")

		if _, err := (&cloudflare{}).ReconcilePreviewWildcard(t.Context(), previewWildcardSpec()); err == nil {
			t.Fatal("ReconcilePreviewWildcard without an account id err = nil, want an error")
		}
	})
}

func TestDestroyPreviewWildcard(t *testing.T) {
	t.Setenv(envAccountID, "acct")

	t.Run("the wildcard route and the script go, the record stays", func(t *testing.T) {
		m := &cfMock{
			bootstrapInstalled: true,
			zoneID:             "zone1",
			zoneName:           "app.com",
			existingRoutes: []map[string]any{
				{"id": "entry", "pattern": "*.preview.app.com/*", "script": previewEntryScriptName},
				{"id": "project", "pattern": "pr-1-abc1234567.preview.app.com/*", "script": "ocel-shop-preview"},
			},
			existingRecords: []map[string]any{
				{"id": "wildcard", "name": "*.preview.app.com", "type": "AAAA", "content": "100::", "comment": dnsWriter(testOrigin).comment(), "proxied": true},
				{"id": "projectrec", "name": "pr-1-abc1234567.preview.app.com", "type": "AAAA", "content": "100::", "comment": dnsWriter(testOrigin).comment(), "proxied": true},
			},
		}

		if err := m.provider(t).DestroyPreviewWildcard(t.Context(), "preview.app.com"); err != nil {
			t.Fatalf("DestroyPreviewWildcard: %v", err)
		}

		assertSet(t, "deleted routes", m.deletedRoutes, []string{"entry"})
		assertSet(t, "deleted records", m.deletedRecords, nil)
		assertSet(t, "deleted scripts", m.deletedScripts, []string{previewEntryScriptName})
	})

	t.Run("the script stays while another wildcard still routes to it", func(t *testing.T) {
		m := &cfMock{
			bootstrapInstalled: true,
			zoneID:             "zone1",
			zoneName:           "app.com",
			existingRoutes: []map[string]any{
				{"id": "entry", "pattern": "*.preview.app.com/*", "script": previewEntryScriptName},
				{"id": "sibling", "pattern": "*.staging.app.com/*", "script": previewEntryScriptName},
			},
		}

		if err := m.provider(t).DestroyPreviewWildcard(t.Context(), "preview.app.com"); err != nil {
			t.Fatalf("DestroyPreviewWildcard: %v", err)
		}

		assertSet(t, "deleted routes", m.deletedRoutes, []string{"entry"})
		assertSet(t, "deleted scripts", m.deletedScripts, nil)
	})

	t.Run("another origin's preview entry is neither unrouted nor what keeps this one's", func(t *testing.T) {
		theirs := "ocel-preview-32da6c-shared-entry"
		m := &cfMock{
			bootstrapInstalled: true,
			zoneID:             "zone1",
			zoneName:           "app.com",
			existingRoutes: []map[string]any{
				{"id": "entry", "pattern": "*.preview.app.com/*", "script": previewEntryScriptName},
				{"id": "theirs", "pattern": "*.staging.app.com/*", "script": theirs},
			},
		}

		if err := m.provider(t).DestroyPreviewWildcard(t.Context(), "preview.app.com"); err != nil {
			t.Fatalf("DestroyPreviewWildcard: %v", err)
		}

		assertSet(t, "deleted routes", m.deletedRoutes, []string{"entry"})
		assertSet(t, "deleted scripts", m.deletedScripts, []string{previewEntryScriptName})
	})

	t.Run("a wildcard another script owns is left in place", func(t *testing.T) {
		m := &cfMock{
			bootstrapInstalled: true,
			zoneID:             "zone1",
			zoneName:           "app.com",
			existingRoutes: []map[string]any{
				{"id": "someone-elses", "pattern": "*.preview.app.com/*", "script": "ocel-shop-preview"},
			},
			existingRecords: []map[string]any{
				{"id": "wildcard", "name": "*.preview.app.com", "type": "AAAA", "content": "100::", "comment": dnsWriter(testOrigin).comment(), "proxied": true},
			},
		}

		if err := m.provider(t).DestroyPreviewWildcard(t.Context(), "preview.app.com"); err != nil {
			t.Fatalf("DestroyPreviewWildcard: %v", err)
		}
		if len(m.deletedRoutes) != 0 {
			t.Errorf("deleted routes = %v, want none: the wildcard belongs to another worker", m.deletedRoutes)
		}
		assertSet(t, "deleted scripts", m.deletedScripts, []string{previewEntryScriptName})
	})

	t.Run("an unset account id is an error", func(t *testing.T) {
		t.Setenv(envAccountID, "")

		if err := (&cloudflare{}).DestroyPreviewWildcard(t.Context(), "preview.app.com"); err == nil {
			t.Fatal("DestroyPreviewWildcard without an account id err = nil, want an error")
		}
	})
}

func TestPruneStaleRoutesSparesThePreviewEntry(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		script string
		spec   routeSpec
	}{
		{
			name:   "a project's script never sweeps it",
			script: "ocel-preview",
			spec:   routedSpec([]string{"*.preview.app.com"}, "pr-1-abc1234567.preview.app.com"),
		},
		{
			name:   "the shared script does not sweep its own route",
			script: previewEntryScriptName,
			spec:   routedSpec([]string{"*.preview.app.com"}, "pr-1-abc1234567.preview.app.com"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := &cfMock{
				bootstrapInstalled: true,
				zoneID:             "zone1",
				zoneName:           "app.com",
				existingRoutes: []map[string]any{
					{"id": "entry", "pattern": "*.preview.app.com/*", "script": previewEntryScriptName},
				},
				existingRecords: []map[string]any{
					{"id": "wildcard", "name": "*.preview.app.com", "type": "AAAA", "content": "100::", "comment": dnsWriter(testOrigin).comment(), "proxied": true},
				},
			}

			if err := m.provider(t).reconcileWorkerRoutes(t.Context(), testNames, upload{accountID: "acct", scriptName: tc.script}, tc.spec, nil); err != nil {
				t.Fatalf("reconcileWorkerRoutes: %v", err)
			}
			if len(m.deletedRoutes) != 0 {
				t.Errorf("deleted routes = %v, want none: a project deploy must never unhook the shared preview entry", m.deletedRoutes)
			}
		})
	}
}
