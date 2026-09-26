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

	"github.com/ocelhq/ocel/pkg/constants"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/appbuild"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/images"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/providerserver"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func builtTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func walked(t *testing.T, dir string) []string {
	t.Helper()
	rels, err := images.ArtifactFiles(dir)
	if err != nil {
		t.Fatalf("artifactFiles: %v", err)
	}
	return rels
}

func digested(t *testing.T, dir string, overlay map[string][]byte) string {
	t.Helper()
	sum, err := providerserver.DigestArtifact(dir, walked(t, dir), overlay)
	if err != nil {
		t.Fatalf("digestArtifact: %v", err)
	}
	return sum
}

func TestAnArtifactDigestNamesOneTreeAndOneOverlay(t *testing.T) {
	files := map[string]string{"src/server.js": "handler", "package.json": `{"name":"app"}`}
	sealed := map[string][]byte{constants.ProjectStateDirName + "/vars.sealed": []byte("one")}

	t.Run("one tree built twice digests the same", func(t *testing.T) {
		if a, b := digested(t, builtTree(t, files), nil), digested(t, builtTree(t, files), nil); a != b {
			t.Errorf("two identical trees digest to %q and %q, want one key so an unchanged build is not re-uploaded", a, b)
		}
	})

	t.Run("changed contents digest apart", func(t *testing.T) {
		base := digested(t, builtTree(t, map[string]string{"a.js": "one"}), nil)
		if changed := digested(t, builtTree(t, map[string]string{"a.js": "two"}), nil); base == changed {
			t.Error("the digest ignored a file's contents, so a function would keep serving the code it replaced")
		}
	})

	t.Run("renamed files digest apart", func(t *testing.T) {
		base := digested(t, builtTree(t, map[string]string{"a.js": "one"}), nil)
		if changed := digested(t, builtTree(t, map[string]string{"b.js": "one"}), nil); base == changed {
			t.Error("the digest ignored a rename")
		}
	})

	t.Run("a symlink's target digests apart", func(t *testing.T) {
		linked := func(target string) string {
			dir := builtTree(t, map[string]string{"a.js": "x", "b.js": "x"})
			if err := os.Symlink(target, filepath.Join(dir, "link.js")); err != nil {
				t.Fatal(err)
			}
			return digested(t, dir, nil)
		}
		if linked("a.js") == linked("b.js") {
			t.Error("the digest ignored the symlink target")
		}
	})

	t.Run("the overlay digests with the tree", func(t *testing.T) {
		dir := builtTree(t, files)
		bare, with := digested(t, dir, nil), digested(t, dir, sealed)
		if bare == with {
			t.Error("the digest ignored the overlay, so a resealed package would land on the key the old one holds")
		}
		if again := digested(t, dir, sealed); again != with {
			t.Errorf("one tree and one overlay digest to %q then %q, want one key", with, again)
		}
		other := digested(t, dir, map[string][]byte{constants.ProjectStateDirName + "/vars.sealed": []byte("two")})
		if other == with {
			t.Error("the digest ignored the overlay's contents")
		}
	})
}

func packed(t *testing.T, dir string, overlay map[string][]byte) *zip.Reader {
	t.Helper()
	path, err := providerserver.PackArtifact(dir, walked(t, dir), overlay)
	if err != nil {
		t.Fatalf("packArtifact: %v", err)
	}
	t.Cleanup(func() { os.Remove(path) })
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	size, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(file, size)
	if err != nil {
		t.Fatalf("the packed artifact is not a zip: %v", err)
	}
	return archive
}

func TestAPackedArtifactCarriesTheTreeTheOverlayAndItsSymlinks(t *testing.T) {
	t.Run("the tree and the overlay round trip", func(t *testing.T) {
		dir := builtTree(t, map[string]string{"src/server.js": "handler", "package.json": "{}"})
		files := map[string]string{}
		for _, entry := range packed(t, dir, map[string][]byte{constants.ProjectStateDirName + "/vars.sealed": []byte("sealed")}).File {
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
		want := map[string]string{
			"src/server.js": "handler",
			"package.json":  "{}",
			constants.ProjectStateDirName + "/vars.sealed": "sealed",
		}
		for name, body := range want {
			if files[name] != body {
				t.Errorf("the package holds %s = %q, want %q", name, files[name], body)
			}
		}
		if len(files) != len(want) {
			t.Errorf("the package holds %v, want exactly %v", files, want)
		}
	})

	t.Run("a symlink stays a symlink", func(t *testing.T) {
		dir := builtTree(t, map[string]string{"real.js": "module.exports={}"})
		if err := os.Symlink("real.js", filepath.Join(dir, "link.js")); err != nil {
			t.Fatal(err)
		}
		entries := map[string]*zip.File{}
		for _, entry := range packed(t, dir, nil).File {
			entries[entry.Name] = entry
		}
		link, held := entries["link.js"]
		if !held {
			t.Fatal("the package dropped the symlink")
		}
		if link.Mode()&os.ModeSymlink == 0 {
			t.Errorf("link.js packed as %v, want a symlink: a resolved copy doubles the package a function boots from", link.Mode())
		}
		body, err := link.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer body.Close()
		target, err := io.ReadAll(body)
		if err != nil {
			t.Fatal(err)
		}
		if string(target) != "real.js" {
			t.Errorf("the symlink points at %q, want %q", target, "real.js")
		}
		if entries["real.js"].Mode()&os.ModeSymlink != 0 {
			t.Error("real.js packed as a symlink, want the regular file it is")
		}
	})
}

const sealedFile = constants.ProjectStateDirName + "/vars.sealed"

type packingProvider struct {
	*fake.Provider

	mu       sync.Mutex
	requests []provider.PackAppRequest
}

func (p *packingProvider) Hooks() provider.Hooks {
	hooks := p.Provider.Hooks()
	hooks.PackApp = p.PackApp
	return hooks
}

func (p *packingProvider) PackApp(_ context.Context, req provider.PackAppRequest, _ edge.Progress) (provider.PackAppResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, req)
	return provider.PackAppResult{
		Overlay:     map[string][]byte{sealedFile: []byte("sealed for " + req.App)},
		VendorState: "bundle for " + req.App,
	}, nil
}

func (p *packingProvider) packings() []provider.PackAppRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]provider.PackAppRequest(nil), p.requests...)
}

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

func TestDeployPacksTheVendorsOverlayIntoEveryFunctionPackage(t *testing.T) {
	builtProject(t)
	provider := &packingProvider{Provider: fake.NewProvider(fake.Options{})}
	client := servedBy(t, provider)
	standsBootstrapped(t, client)

	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	spec := provider.FakeStacks().Provisioned()[1]
	files := packagedFiles(t, provider, spec.App.Functions[0].Artifact)
	if _, held := files[builtEntrypoint]; !held {
		t.Errorf("the package holds %v, want the built artifact's own files", files)
	}
	if got := files[sealedFile]; got != "sealed for web" {
		t.Errorf("the package holds %s = %q, want the sealed values the vendor packed; without them the function boots with no variables", sealedFile, got)
	}
	if spec.App.VendorState != "bundle for web" {
		t.Errorf("the app spec carries %v, want what the pack handed back: the stack's env must pair with the package it sealed", spec.App.VendorState)
	}

	requests := provider.packings()
	if len(requests) != 1 || requests[0].App != "web" || requests[0].Values.Folder != spec.App.Values.Folder {
		t.Errorf("the vendor was asked to pack %+v, want the app's own values, once", requests)
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
		Framework:    &contractv1.Framework{Name: "next"},
		Handler:      "index.handler",
		ArtifactPath: adminArtifactPath,
	})

	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	spec := provider.FakeStacks().Provisioned()[1]
	packages := map[string]map[string]string{}
	for _, fn := range spec.App.Functions {
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
	provider := &countingProvider{Provider: fake.NewProvider(fake.Options{}), puts: map[string]int{}}
	provider.WithArtifactStore(countedArtifacts{ArtifactStore: provider.Artifacts(), on: provider})
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
	dir := filepath.Join(appbuild.ArtifactRoot(), filepath.FromSlash(path))
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
			Framework:    &contractv1.Framework{Name: "next"},
			Handler:      "index.handler",
			ArtifactPath: builtFunction(t, route),
		})
	}

	base := fake.NewProvider(fake.Options{})
	provider := &barrierProvider{
		Provider: base,
		store:    &barrierArtifacts{ArtifactStore: base.Artifacts(), want: functions, ready: make(chan struct{})},
	}
	provider.WithArtifactStore(provider.store)
	client := servedBy(t, provider)

	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	if provider.store.peak != functions {
		t.Errorf("at most %d of the app's %d functions were uploading at once, want them in flight together: an app of many functions waits one round trip at a time otherwise",
			provider.store.peak, functions)
	}
}
