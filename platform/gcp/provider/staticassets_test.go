package gcp

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

func plantStatic(t *testing.T, p *Provider, files map[string]string) {
	t.Helper()
	root, err := buildoutput.Root(p.projectDir)
	if err != nil {
		t.Fatal(err)
	}
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
}

func staticSpec() provider.StackSpec {
	spec := routedNextSpec()
	spec.App.Static = &edge.Static{ImmutablePrefixes: []string{"/_next/static/"}}
	return spec
}

func uploadNamed(t *testing.T, uploads []upload, name string) upload {
	t.Helper()
	for _, u := range uploads {
		if u.name == name {
			return u
		}
	}
	t.Fatalf("no upload named %s among %v", name, uploadNames(uploads))
	return upload{}
}

func TestANextDeployUploadsItsStaticFilesAndImageConfigUnderTheReleasesStoragePrefix(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	plantStatic(t, p, map[string]string{
		"static/_next/static/app.js": "chunk",
		"static/logo.svg":            "<svg/>",
		"image-config.json":          "{}",
		"routing.json":               "{}",
	})

	if _, err := p.ProvisionFunctions(context.Background(), staticSpec(), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	names := uploadNames(server.stored())
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
	server := &runServer{}
	p := server.open(t)
	plantStatic(t, p, map[string]string{"static/_next/static/app.js": "chunk", "static/logo.svg": "<svg/>"})

	if _, err := p.ProvisionFunctions(context.Background(), staticSpec(), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	uploads := server.stored()
	chunk := uploadNamed(t, uploads, "assets/prod/shop/web/r1/assets/_next/static/app.js")
	if chunk.contentType != "text/javascript; charset=utf-8" || chunk.cacheControl != edge.ImmutableCacheControl {
		t.Errorf("the chunk is %q cached as %q, want its type and the immutable cache control", chunk.contentType, chunk.cacheControl)
	}
	logo := uploadNamed(t, uploads, "assets/prod/shop/web/r1/assets/logo.svg")
	if logo.contentType != "image/svg+xml" || logo.cacheControl != edge.RevalidateCacheControl {
		t.Errorf("the logo is %q cached as %q, want its type and the revalidating cache control", logo.contentType, logo.cacheControl)
	}
	if string(logo.body) != "<svg/>" {
		t.Errorf("the logo's bytes = %q, want the file's", logo.body)
	}
}

func TestTheStaticFilesAreUploadedBeforeTheServiceIsCreated(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	plantStatic(t, p, map[string]string{"static/logo.svg": "<svg/>"})

	if _, err := p.ProvisionFunctions(context.Background(), staticSpec(), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	events := server.happened()
	if len(events) < 2 || events[len(events)-1] != "create" {
		t.Errorf("the deploy did %v, want every upload before the service is created", events)
	}
}

func TestARedeployOfTheSameReleaseKeepsTheStaticFilesItHas(t *testing.T) {
	const logo = "assets/prod/shop/web/r1/assets/logo.svg"
	server := &runServer{present: map[string]bool{logo: true}}
	p := server.open(t)
	plantStatic(t, p, map[string]string{"static/logo.svg": "<svg/>"})

	if _, err := p.ProvisionFunctions(context.Background(), staticSpec(), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	if got := uploadNamed(t, server.stored(), logo).ifGenerationMatch; got != "0" {
		t.Errorf("the logo was written with ifGenerationMatch=%q, want 0 so a redeploy keeps what is there", got)
	}
}

func TestAServiceThatRoutesNothingUploadsNoStaticFiles(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	plantStatic(t, p, map[string]string{"static/logo.svg": "<svg/>", "image-config.json": "{}"})

	if _, err := p.ProvisionFunctions(context.Background(), nextSpec(), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	if got := server.stored(); len(got) != 0 {
		t.Errorf("an app that routes nothing uploaded %v, want nothing", uploadNames(got))
	}
}

func TestAStaticFilesAssetPrefixThatIsNotAReleasesAssetsIsRefused(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	plantStatic(t, p, map[string]string{"static/logo.svg": "<svg/>"})
	spec := staticSpec()
	spec.App.AssetPrefix = "prod/shop/web/r1/files"

	if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err == nil {
		t.Fatal("ProvisionFunctions() = nil, want a refusal: the router reads assets under the prefix and the image config beside it")
	}
}

func TestANextServiceIsToldTheBucketItsStaticFilesLiveIn(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	if _, err := p.ProvisionFunctions(context.Background(), staticSpec(), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	env := envOf(server.created[0].Template.Containers[0])

	if got, want := env["OCEL_ASSET_BUCKET"], names(t, p).Bucket(environment.TierProduction); got != want {
		t.Errorf("the Next service reads OCEL_ASSET_BUCKET=%q, want its tier's bucket %q", got, want)
	}
	if got, told := env["OCEL_STATIC_DIR"]; told {
		t.Errorf("the Next service reads OCEL_STATIC_DIR=%q, but its image holds no static files", got)
	}
}

func TestAnAppsAccountMayReadItsOwnStaticFilesAndNoOtherApps(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	if _, err := p.ProvisionFunctions(context.Background(), staticSpec(), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	var granted []string
	for _, binding := range projectBindingsOf(server.identities()) {
		if strings.HasPrefix(binding, appAssetsRole+" ") {
			granted = append(granted, binding)
		}
	}
	want := appAssetsRole + " serviceAccount:" + names(t, p).AppAccountEmail(environment.TierProduction, "shop", "web") +
		` resource.name.startsWith("projects/_/buckets/` + names(t, p).Bucket(environment.TierProduction) + `/objects/assets/prod/shop/web/")`
	if !slices.Equal(granted, []string{want}) {
		t.Errorf("the app's asset bindings = %q, want exactly %q", granted, want)
	}
}
