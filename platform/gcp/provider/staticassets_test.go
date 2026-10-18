package gcp

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func plantStatic(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	app := buildoutput.AppRoot(root, "web")
	for rel, body := range files {
		path := filepath.Join(app, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func staticSpec() provider.StackSpec {
	spec := routedNextSpec()
	spec.App.Static = &edge.Static{ImmutablePrefixes: []string{"/_next/static/"}}
	return spec
}

func uploadNamed(t *testing.T, uploads []googletest.Upload, name string) googletest.Upload {
	t.Helper()
	for _, u := range uploads {
		if u.Name == name {
			return u
		}
	}
	t.Fatalf("no upload named %s among %v", name, uploadNames(uploads))
	return googletest.Upload{}
}

func TestANextDeployUploadsItsStaticFilesAndImageConfigUnderTheReleasesStoragePrefix(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	root := plantStatic(t, map[string]string{
		"static/_next/static/app.js": "chunk",
		"static/logo.svg":            "<svg/>",
		"image-config.json":          "{}",
		"routing.json":               "{}",
	})

	if _, err := p.ProvisionFunctions(context.Background(), builtAt(staticSpec(), root), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	names := uploadNames(server.Stored())
	slices.Sort(names)
	want := []string{
		"assets/prod/shop/web/r1/assets/_next/static/app.js",
		"assets/prod/shop/web/r1/assets/logo.svg",
		"assets/prod/shop/web/r1/image-config.json",
	}
	if !slices.Equal(names, want) {
		t.Errorf("the deploy uploaded %v, want %v", names, want)
	}
}

func TestAStaticFileIsUploadedTypedAndCachedAsTheRouterServesIt(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	root := plantStatic(t, map[string]string{"static/_next/static/app.js": "chunk", "static/logo.svg": "<svg/>"})

	if _, err := p.ProvisionFunctions(context.Background(), builtAt(staticSpec(), root), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	uploads := server.Stored()
	chunk := uploadNamed(t, uploads, "assets/prod/shop/web/r1/assets/_next/static/app.js")
	if chunk.ContentType != "text/javascript; charset=utf-8" || chunk.CacheControl != edge.ImmutableCacheControl {
		t.Errorf("the chunk is %q cached as %q, want its type and the immutable cache control", chunk.ContentType, chunk.CacheControl)
	}
	logo := uploadNamed(t, uploads, "assets/prod/shop/web/r1/assets/logo.svg")
	if logo.ContentType != "image/svg+xml" || logo.CacheControl != edge.RevalidateCacheControl {
		t.Errorf("the logo is %q cached as %q, want its type and the revalidating cache control", logo.ContentType, logo.CacheControl)
	}
	if string(logo.Body) != "<svg/>" {
		t.Errorf("the logo's bytes = %q, want the file's", logo.Body)
	}
}

func TestTheStaticFilesAreUploadedBeforeTheServiceIsCreated(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	root := plantStatic(t, map[string]string{"static/logo.svg": "<svg/>"})

	if _, err := p.ProvisionFunctions(context.Background(), builtAt(staticSpec(), root), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	events := server.Happened()
	if len(events) < 2 || events[len(events)-1] != "create" {
		t.Errorf("the deploy did %v, want every upload before the service is created", events)
	}
}

func TestARedeployOfTheSameReleaseUploadsOnlyTheStaticFilesItLacks(t *testing.T) {
	const logo = "assets/prod/shop/web/r1/assets/logo.svg"
	const chunk = "assets/prod/shop/web/r1/assets/_next/static/app.js"
	server := &googletest.Run{Present: map[string]bool{logo: true}}
	p := providerServing(server, t)
	root := plantStatic(t, map[string]string{"static/logo.svg": "<svg/>", "static/_next/static/app.js": "chunk"})

	if _, err := p.ProvisionFunctions(context.Background(), builtAt(staticSpec(), root), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	if names := uploadNames(server.Stored()); !slices.Equal(names, []string{chunk}) {
		t.Errorf("the redeploy uploaded %v, want only the missing %s", names, chunk)
	}
	if got := uploadNamed(t, server.Stored(), chunk).IfGenerationMatch; got != "0" {
		t.Errorf("the chunk was written with ifGenerationMatch=%q, want 0 so a concurrent deploy keeps what it wrote", got)
	}
}

func TestARedeployOfTheSameReleaseReplacesItsImageConfig(t *testing.T) {
	const imageConfig = "assets/prod/shop/web/r1/image-config.json"
	server := &googletest.Run{Present: map[string]bool{imageConfig: true}}
	p := providerServing(server, t)
	root := plantStatic(t, map[string]string{"image-config.json": `{"sizes":[640]}`})

	if _, err := p.ProvisionFunctions(context.Background(), builtAt(staticSpec(), root), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	if got := uploadNamed(t, server.Stored(), imageConfig).IfGenerationMatch; got != "" {
		t.Errorf("the image config was written with ifGenerationMatch=%q, want no condition so a redeploy replaces it", got)
	}
}

func TestAServiceThatRoutesNothingUploadsNoStaticFiles(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	root := plantStatic(t, map[string]string{"static/logo.svg": "<svg/>", "image-config.json": "{}"})

	if _, err := p.ProvisionFunctions(context.Background(), builtAt(nextSpec(), root), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	if got := server.Stored(); len(got) != 0 {
		t.Errorf("an app that routes nothing uploaded %v, want nothing", uploadNames(got))
	}
}

func TestAStaticFilesAssetPrefixThatIsNotAReleasesAssetsIsRefused(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	root := plantStatic(t, map[string]string{"static/logo.svg": "<svg/>"})
	spec := staticSpec()
	spec.App.AssetPrefix = "prod/shop/web/r1/files"

	if _, err := p.ProvisionFunctions(context.Background(), builtAt(spec, root), nil); err == nil {
		t.Fatal("ProvisionFunctions() = nil, want a refusal: the router reads assets under the prefix and the image config beside it")
	}
}

func TestANextServiceIsToldTheBucketItsStaticFilesLiveIn(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	if _, err := p.ProvisionFunctions(context.Background(), staticSpec(), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	env := googletest.EnvOf(server.Created[0].Template.Containers[0])

	if got, want := env["OCEL_ASSET_BUCKET"], namesOf(t, p).Bucket(environment.TierProduction); got != want {
		t.Errorf("the Next service reads OCEL_ASSET_BUCKET=%q, want its tier's bucket %q", got, want)
	}
}
