package providerkit_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
)

type imaging struct {
	*fake.Provider

	base v1.Image
}

func (i imaging) FunctionBase(context.Context, providerkit.Runtime) (v1.Image, error) {
	if i.base != nil {
		return i.base, nil
	}
	return empty.Image, nil
}

func stagedProject(t *testing.T, apps ...string) {
	t.Helper()
	builtApps(t, apps...)
	for _, app := range apps {
		dir := filepath.Join(providerkit.ArtifactRoot(), filepath.FromSlash(appArtifactPath(app)))
		raw, err := json.Marshal(map[string]any{
			"runtime": map[string]string{"name": "node", "arch": "x86_64"},
			"handler": "index.handler",
			"id":      "server",
			"app":     app,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func imagingDeployRequest() *contractv1.DeployRequest {
	req := namingARegistry(deployRequest())
	req.Manifest.Functions[0].Runtime = &contractv1.Runtime{Name: "node", Arch: "x86_64"}
	return req
}

func imagingServed(t *testing.T) (contractv1connect.ProviderServiceClient, *fake.Provider) {
	t.Helper()
	base := fake.NewProvider(fake.Options{})
	served := servedBy(t, imaging{Provider: base})
	standsBootstrapped(t, served)
	return served, base
}

func TestDeployShipsAFunctionAsAnImageWhereTheProviderTakesItThatWay(t *testing.T) {
	stagedProject(t, "web", "admin")
	served, provider := imagingServed(t)

	result, _ := deploy(t, served, imagingDeployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	pushed := provider.Registry().Pushed()
	if len(pushed) != 1 {
		t.Fatalf("the deploy pushed %v, want the one image the app's function runs", pushed)
	}
	if !strings.HasPrefix(pushed[0].Target, "ghcr.io/acme/") {
		t.Errorf("the push wrote %q, want it under the registry the deploy names", pushed[0].Target)
	}

	for _, ref := range provider.Artifacts().(*fake.Artifacts).Keys() {
		if ref.Bucket == providerkit.StoreFunctions {
			t.Errorf("the deploy uploaded %s to the function store, want the image to be the only thing shipped", ref.Key)
		}
	}

	plans := provider.Releaser().Plans()
	app := plans[len(plans)-1]
	if len(app.Uploads) != 0 {
		t.Errorf("the app plan carries %d uploads, want none: the function travels as an image", len(app.Uploads))
	}
	if len(app.App.Functions) != 1 {
		t.Fatalf("the app plan carries %d functions, want the one the manifest declares", len(app.App.Functions))
	}
	spec := app.App.Functions[0]
	if spec.Image == "" {
		t.Fatal("the function spec names no image, so the provider has nothing to run it from")
	}
	if !strings.Contains(spec.Image, "@sha256:") {
		t.Errorf("the function spec runs %q, want a digest-pinned ref: a tag can move under a revision that is meant to be fixed", spec.Image)
	}
	if spec.Artifact.Key != "" {
		t.Errorf("the function spec names artifact %q as well as an image, want the image alone", spec.Artifact.Key)
	}
}
