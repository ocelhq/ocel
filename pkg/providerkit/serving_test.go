package providerkit_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func servingRoot(t *testing.T, app string, desc edge.ServeDescriptor, manifest []byte) string {
	t.Helper()
	root := t.TempDir()
	dir := providerkit.AppArtifactRoot(root, app)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(desc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, edge.ServeDescriptorFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if manifest != nil {
		if err := os.WriteFile(filepath.Join(dir, edge.RoutingManifestFile), manifest, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func servingQuery(root, app, runtime string) providerkit.ServingQuery {
	return providerkit.ServingQuery{
		Root:       root,
		Project:    "shop",
		App:        app,
		Runtime:    runtime,
		Stack:      naming.AppStack("production", app, naming.NewRelease("dep1", "fp1")),
		Coordinate: naming.Coordinate{Project: "shop", Env: "production", App: app, Release: naming.NewRelease("dep1", "fp1")},
	}
}

func TestEveryAppCarriesTheAssetPrefixAndBytecodeCacheItServesFrom(t *testing.T) {
	facts, err := providerkit.ServingFactsFor(servingQuery(t.TempDir(), "web", "astro"))
	if err != nil {
		t.Fatalf("ServingFactsFor() = %v", err)
	}
	if facts.AssetPrefix == "" {
		t.Error("an app with no asset prefix serves its static files from nowhere")
	}
	if facts.Bytecode == nil || facts.Bytecode.Prefix == "" {
		t.Fatalf("Bytecode = %+v, want a prefix every runtime can warm a cache under", facts.Bytecode)
	}
	if strings.HasSuffix(facts.Bytecode.Prefix, naming.PathSeparator) {
		t.Errorf("Bytecode.Prefix = %q, want it free of the trailing separator a key policy appends", facts.Bytecode.Prefix)
	}
	if facts.Bytecode.Prefix == facts.AssetPrefix {
		t.Error("the bytecode cache and the static assets share a prefix; a deploy would sweep one with the other")
	}
}

func TestOnlyNextAsksForAnISRLedger(t *testing.T) {
	next, err := providerkit.ServingFactsFor(servingQuery(t.TempDir(), "web", providerkit.RuntimeNext))
	if err != nil {
		t.Fatalf("ServingFactsFor() = %v", err)
	}
	if next.ISR == nil || next.ISR.Prefix == "" || next.ISR.TagNamespace == "" {
		t.Fatalf("ISR = %+v, want the prefix and tag namespace a revalidation writes through", next.ISR)
	}
	other, err := providerkit.ServingFactsFor(servingQuery(t.TempDir(), "web", "astro"))
	if err != nil {
		t.Fatalf("ServingFactsFor() = %v", err)
	}
	if other.ISR != nil {
		t.Errorf("ISR = %+v for a runtime that revalidates nothing, want none", other.ISR)
	}
}

func TestAnAppRoutingAtItsOriginCarriesTheManifestItRoutesBy(t *testing.T) {
	manifest := []byte(`{"routes":[]}`)
	root := servingRoot(t, "web", edge.ServeDescriptor{EdgeRouting: true, Entry: "index"}, manifest)

	facts, err := providerkit.ServingFactsFor(servingQuery(root, "web", providerkit.RuntimeNext))
	if err != nil {
		t.Fatalf("ServingFactsFor() = %v", err)
	}
	if facts.Routing == nil {
		t.Fatal("Routing = nil for an app whose build says it routes at its origin")
	}
	if facts.Routing.Entry != "index" {
		t.Errorf("Routing.Entry = %q, want the entry route the build named", facts.Routing.Entry)
	}
	if !bytes.Equal(facts.Routing.Manifest, manifest) {
		t.Errorf("Routing.Manifest = %q, want the bytes the build wrote", facts.Routing.Manifest)
	}
}

func TestAnEdgeThatRunsCodeTakesTheManifestTheOriginWouldHaveRoutedBy(t *testing.T) {
	manifest := []byte(`{"routes":[]}`)
	root := servingRoot(t, "web", edge.ServeDescriptor{EdgeRouting: true, Entry: "index"}, manifest)
	query := servingQuery(root, "web", providerkit.RuntimeNext)
	query.EdgeRunsCode = true

	facts, err := providerkit.ServingFactsFor(query)
	if err != nil {
		t.Fatalf("ServingFactsFor() = %v", err)
	}
	if facts.Routing != nil {
		t.Errorf("Routing = %+v where the edge runs the code, want the origin left out of routing", facts.Routing)
	}
	if facts.EdgeRouting == nil || !bytes.Equal(facts.EdgeRouting.Manifest, manifest) {
		t.Fatalf("EdgeRouting = %+v, want the manifest the edge serves static assets and routes by", facts.EdgeRouting)
	}
}

func TestAnEdgeThatRunsNoCodeHandsTheEdgeNothingToRouteBy(t *testing.T) {
	root := servingRoot(t, "web", edge.ServeDescriptor{EdgeRouting: true, Entry: "index"}, []byte(`{}`))

	facts, err := providerkit.ServingFactsFor(servingQuery(root, "web", providerkit.RuntimeNext))
	if err != nil {
		t.Fatalf("ServingFactsFor() = %v", err)
	}
	if facts.EdgeRouting != nil {
		t.Errorf("EdgeRouting = %+v where the origin routes, want the edge left out of routing", facts.EdgeRouting)
	}
}

func TestAnAppThatRoutesAtItsOriginAndWroteNoManifestIsRefused(t *testing.T) {
	root := servingRoot(t, "web", edge.ServeDescriptor{EdgeRouting: true, Entry: "index"}, nil)

	_, err := providerkit.ServingFactsFor(servingQuery(root, "web", providerkit.RuntimeNext))
	if err == nil || !strings.Contains(err.Error(), edge.RoutingManifestFile) {
		t.Fatalf("ServingFactsFor() = %v, want a refusal naming %s", err, edge.RoutingManifestFile)
	}
}

func TestAnAppThatRoutesAtItsOriginAndNamesNoEntryIsRefused(t *testing.T) {
	root := servingRoot(t, "web", edge.ServeDescriptor{EdgeRouting: true}, []byte(`{}`))

	_, err := providerkit.ServingFactsFor(servingQuery(root, "web", providerkit.RuntimeNext))
	if err == nil || !strings.Contains(err.Error(), "entry route") {
		t.Fatalf("ServingFactsFor() = %v, want a refusal naming the missing entry route", err)
	}
}
