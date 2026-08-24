package providerkit_test

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"sync"
	"testing"

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
		Runtime:      "nodejs22.x",
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
