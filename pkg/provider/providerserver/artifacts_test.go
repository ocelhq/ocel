package providerserver_test

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

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func packagedFiles(t *testing.T, p provider.Provider, ref provider.ArtifactRef) map[string]string {
	t.Helper()
	opened, err := p.Artifacts().Open(context.Background(), ref)
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

func TestDeployPacksTheRoutingManifestIntoTheEntryFunctionAlone(t *testing.T) {
	builtProject(t)
	routing := []byte(`{"routes":[{"id":"index"}]}`)
	builtRoutingApp(t, "web", buildoutput.Hosting{RouteTable: edge.RouteTableNext, RootFunction: "index", FrameworkBuildID: "b1"}, routing)

	vendor := fake.NewProvider(fake.Options{})
	client := servedBy(t, vendor)
	bootstrappedOverRPC(t, client)

	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindDirect)}
	web := webFunctions(req)
	web.Functions[0].RouteId = "index"
	web.Functions = append(web.Functions, &contractv1.ManifestFunction{
		LogicalName:  "feed",
		RouteId:      "feed",
		Framework:    &contractv1.Framework{Name: "next"},
		EntryFile:    "index.handler",
		ArtifactPath: adminArtifactPath,
	})

	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	spec := vendor.FakeStacks().Provisioned()[1]
	packages := map[string]map[string]string{}
	for _, shipped := range vendor.FakeStacks().Shipped(spec.Ref) {
		packages[shipped.Name] = packagedFiles(t, vendor, shipped.Ref)
	}
	if got := packages["server"][edge.NextRouteTableFile]; got != string(routing) {
		t.Errorf("the root function's package contains %s = %q, want the route table it routes the app with", edge.NextRouteTableFile, got)
	}
	if _, ok := packages["feed"][edge.NextRouteTableFile]; ok {
		t.Errorf("a function that routes nothing contains %s", edge.NextRouteTableFile)
	}
}

func TestDeployPacksTheOcelRouteTableIntoEveryFunctionOfTheApp(t *testing.T) {
	builtProject(t)
	builtRoutingApp(t, "web", buildoutput.Hosting{RouteTable: edge.RouteTableOcel, RootFunction: "index", FrameworkBuildID: "b1", Needs: revalidating}, []byte(cachedRoutes))

	vendor := fake.NewProvider(fake.Options{})
	client := servedBy(t, vendor)
	bootstrappedOverRPC(t, client)

	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindDirect)}
	web := webFunctions(req)
	web.Functions[0].RouteId = "index"
	web.Functions = append(web.Functions, &contractv1.ManifestFunction{
		LogicalName:  "feed",
		RouteId:      "feed",
		Framework:    &contractv1.Framework{Name: "next"},
		EntryFile:    "index.handler",
		ArtifactPath: adminArtifactPath,
	})

	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	spec := vendor.FakeStacks().Provisioned()[1]
	for _, shipped := range vendor.FakeStacks().Shipped(spec.Ref) {
		if got := packagedFiles(t, vendor, shipped.Ref)[edge.OcelRouteTableFile]; got != cachedRoutes {
			t.Errorf("%s's package contains %s = %q, want the table every function of the app reads", shipped.Name, edge.OcelRouteTableFile, got)
		}
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
	provider.ArtifactStore
	on *countingProvider
}

func (c countedArtifacts) Put(ctx context.Context, ref provider.ArtifactRef, body io.Reader) error {
	c.on.mu.Lock()
	c.on.puts[ref.Key]++
	c.on.mu.Unlock()
	return c.ArtifactStore.Put(ctx, ref, body)
}

func TestAnUnchangedBuildIsNotUploadedTwice(t *testing.T) {
	builtProject(t)
	vendor := &countingProvider{Provider: fake.NewProvider(fake.Options{}), puts: map[string]int{}}
	vendor.WithArtifactStore(countedArtifacts{ArtifactStore: vendor.Artifacts(), on: vendor})
	client := servedBy(t, vendor)
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier: environmentv1.Tier_TIER_PREVIEW,
	})

	for range 2 {
		if result, _ := deploy(t, client, previewRequest()); !result.GetSuccess() {
			t.Fatalf("Deploy() = %q", result.GetError())
		}
	}

	uploads := vendor.uploads()
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
	provider.ArtifactStore

	want  int
	ready chan struct{}

	mu    sync.Mutex
	going int
	peak  int
}

func (b *barrierArtifacts) Put(ctx context.Context, ref provider.ArtifactRef, body io.Reader) error {
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
	dir := filepath.Join(workingOutputRoot(t), filepath.FromSlash(path))
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
	web := webFunctions(req)
	web.Functions[0].RouteId = "index"
	web.Functions[0].ArtifactPath = builtFunction(t, "index")
	for i := 1; i < functions; i++ {
		route := fmt.Sprintf("route-%d", i)
		web.Functions = append(web.Functions, &contractv1.ManifestFunction{
			LogicalName:  route,
			RouteId:      route,
			Framework:    &contractv1.Framework{Name: "next"},
			EntryFile:    "index.handler",
			ArtifactPath: builtFunction(t, route),
		})
	}

	base := fake.NewProvider(fake.Options{})
	vendor := &barrierProvider{
		Provider: base,
		store:    &barrierArtifacts{ArtifactStore: base.Artifacts(), want: functions, ready: make(chan struct{})},
	}
	vendor.WithArtifactStore(vendor.store)
	client := servedBy(t, vendor)

	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	if vendor.store.peak != functions {
		t.Errorf("at most %d of the app's %d functions were uploading at once, want them in flight together: an app of many functions waits one round trip at a time otherwise",
			vendor.store.peak, functions)
	}
}
