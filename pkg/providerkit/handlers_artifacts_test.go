package providerkit_test

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const sealedFile = ".ocel/vars.sealed"

type packingProvider struct {
	*fake.Provider

	mu     sync.Mutex
	packed []providerkit.AppPacking
}

func (p *packingProvider) Membrane(context.Context) ([]byte, error) {
	return []byte(fake.Membrane), nil
}

func (p *packingProvider) PackApp(_ context.Context, packing providerkit.AppPacking, _ providerkit.Reporter) (providerkit.AppPack, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.packed = append(p.packed, packing)
	return providerkit.AppPack{
		Overlay: map[string][]byte{sealedFile: []byte("sealed for " + packing.App)},
		Carry:   "bundle for " + packing.App,
	}, nil
}

func (p *packingProvider) packings() []providerkit.AppPacking {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]providerkit.AppPacking(nil), p.packed...)
}

func packagedFiles(t *testing.T, provider providerkit.Provider, ref providerkit.ArtifactRef) map[string]string {
	t.Helper()
	opened, err := provider.Artifacts().Open(context.Background(), ref)
	if err != nil {
		t.Fatalf("Open(%+v) = %v, want the uploaded package", ref, err)
	}
	defer opened.Close()
	raw, err := io.ReadAll(opened)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("the uploaded artifact is not a zip a function can be deployed from: %v", err)
	}
	files := map[string]string{}
	for _, entry := range archive.File {
		body, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(body)
		body.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[entry.Name] = string(content)
	}
	return files
}

func TestDeployPacksTheVendorsOverlayIntoEveryFunctionPackage(t *testing.T) {
	builtProject(t)
	provider := &packingProvider{Provider: fake.NewProvider(fake.Options{})}
	client := servedBy(t, provider)
	standsBootstrapped(t, client)

	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	plan := provider.Releases().(*fake.Releaser).Plans()[1]
	files := packagedFiles(t, provider, plan.App.Functions[0].Artifact)
	if _, held := files[builtEntrypoint]; !held {
		t.Errorf("the package holds %v, want the built artifact's own files", files)
	}
	if got := files[sealedFile]; got != "sealed for web" {
		t.Errorf("the package holds %s = %q, want the sealed values the vendor packed; without them the function boots with no variables", sealedFile, got)
	}
	if plan.App.Packed != "bundle for web" {
		t.Errorf("the app plan carries %v, want what the pack handed back: the stack's env must pair with the package it sealed", plan.App.Packed)
	}

	packings := provider.packings()
	if len(packings) != 1 || packings[0].App != "web" || packings[0].Values.Folder != plan.App.Values.Folder {
		t.Errorf("the vendor was asked to pack %+v, want the app's own values, once", packings)
	}
}

func TestDeployPacksTheRoutingManifestIntoTheEntryFunctionAlone(t *testing.T) {
	builtProject(t)
	routing := []byte(`{"routes":[{"id":"index"}]}`)
	builtRoutingApp(t, "web", edge.ServeDescriptor{EdgeRouting: true, Entry: "index", BuildID: "b1"}, routing)

	provider := &packingProvider{Provider: fake.NewProvider(fake.Options{})}
	client := servedBy(t, provider)
	standsBootstrapped(t, client)

	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindDirect)}
	manifest := req.GetManifest()
	manifest.Functions[0].RouteId = "index"
	manifest.Functions = append(manifest.Functions, &contractv1.ManifestFunction{
		LogicalName:  "feed",
		App:          "web",
		RouteId:      "feed",
		Runtime:      &contractv1.Runtime{Name: "next"},
		Handler:      "index.handler",
		ArtifactPath: adminArtifactPath,
	})

	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	plan := provider.Releases().(*fake.Releaser).Plans()[1]
	packages := map[string]map[string]string{}
	for _, fn := range plan.App.Functions {
		packages[fn.Name] = packagedFiles(t, provider, fn.Artifact)
	}
	if got := packages["server"][edge.RoutingManifestFile]; got != string(routing) {
		t.Errorf("the entry function's package holds %s = %q, want the routing manifest it routes the app with", edge.RoutingManifestFile, got)
	}
	if _, held := packages["feed"][edge.RoutingManifestFile]; held {
		t.Errorf("a function that routes nothing carries %s", edge.RoutingManifestFile)
	}
	if packages["server"][sealedFile] == "" || packages["feed"][sealedFile] == "" {
		t.Error("the sealed values reach only some of the app's functions, want every one of them")
	}
}

func previewRequest() *contractv1.DeployRequest {
	req := deployRequest()
	req.Manifest.Domains = []*contractv1.TierDomains{{
		Tier:      environmentv1.Tier_TIER_PREVIEW,
		Hostnames: []string{"*.preview.example"},
	}}
	req.Environment = &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-7"}
	return req
}

type countingProvider struct {
	*fake.Provider

	mu   sync.Mutex
	puts map[string]int
}

func (p *countingProvider) uploads() map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return maps.Clone(p.puts)
}

type countedArtifacts struct {
	providerkit.ArtifactStore
	on *countingProvider
}

func (c countedArtifacts) Put(ctx context.Context, ref providerkit.ArtifactRef, body io.Reader) error {
	c.on.mu.Lock()
	c.on.puts[ref.Key]++
	c.on.mu.Unlock()
	return c.ArtifactStore.Put(ctx, ref, body)
}

func TestAnUnchangedBuildIsNotUploadedTwice(t *testing.T) {
	builtProject(t)
	provider := &countingProvider{Provider: fake.NewProvider(fake.Options{}), puts: map[string]int{}}
	provider.Ships(countedArtifacts{ArtifactStore: provider.Provider.Artifacts(), on: provider})
	client := servedBy(t, provider)
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PREVIEW,
		Features: []string{fake.FeatureCache, fake.FeatureImages},
	})

	for range 2 {
		if result, _ := deploy(t, client, previewRequest()); !result.GetSuccess() {
			t.Fatalf("Deploy() = %q", result.GetError())
		}
	}

	uploads := provider.uploads()
	if len(uploads) == 0 {
		t.Fatal("the deploy uploaded nothing, so the count proves nothing")
	}
	for key, count := range uploads {
		if count != 1 {
			t.Errorf("%s was uploaded %d times, want one: an unchanged build is already at the digest that names it", key, count)
		}
	}
}

type barrierProvider struct {
	*fake.Provider
	store *barrierArtifacts
}

type barrierArtifacts struct {
	providerkit.ArtifactStore

	want  int
	ready chan struct{}

	mu    sync.Mutex
	going int
	peak  int
}

func (b *barrierArtifacts) Put(ctx context.Context, ref providerkit.ArtifactRef, body io.Reader) error {
	b.mu.Lock()
	b.going++
	b.peak = max(b.peak, b.going)
	if b.going == b.want {
		close(b.ready)
	}
	b.mu.Unlock()

	select {
	case <-b.ready:
	case <-time.After(5 * time.Second):
	}

	b.mu.Lock()
	b.going--
	b.mu.Unlock()
	return b.ArtifactStore.Put(ctx, ref, body)
}

func builtFunction(t *testing.T, name string) string {
	t.Helper()
	path := "apps/web/functions/" + name + ".func"
	dir := filepath.Join(providerkit.ArtifactRoot(), filepath.FromSlash(path))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, builtEntrypoint), []byte("a built "+name), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAnAppsFunctionsAreUploadedTogether(t *testing.T) {
	builtProject(t)
	const functions = 4

	req := deployRequest()
	manifest := req.GetManifest()
	manifest.Functions[0].RouteId = "index"
	manifest.Functions[0].ArtifactPath = builtFunction(t, "index")
	for i := 1; i < functions; i++ {
		route := fmt.Sprintf("route-%d", i)
		manifest.Functions = append(manifest.Functions, &contractv1.ManifestFunction{
			LogicalName:  route,
			App:          "web",
			RouteId:      route,
			Runtime:      &contractv1.Runtime{Name: "next"},
			Handler:      "index.handler",
			ArtifactPath: builtFunction(t, route),
		})
	}

	base := fake.NewProvider(fake.Options{})
	provider := &barrierProvider{
		Provider: base,
		store:    &barrierArtifacts{ArtifactStore: base.Artifacts(), want: functions, ready: make(chan struct{})},
	}
	provider.Ships(provider.store)
	client := servedBy(t, provider)

	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	if provider.store.peak != functions {
		t.Errorf("at most %d of the app's %d functions were uploading at once, want them in flight together: an app of many functions waits one round trip at a time otherwise",
			provider.store.peak, functions)
	}
}
