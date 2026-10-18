package gcp

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func plantPrerenders(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		path := filepath.Join(buildoutput.AppRoot(root, "web"), filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func uploadNames(uploads []googletest.Upload) []string {
	names := make([]string, 0, len(uploads))
	for _, u := range uploads {
		names = append(names, u.Name)
	}
	return names
}

func TestANextDeploySeedsUnderTheCacheStoreProjectFirst(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	root := plantPrerenders(t, map[string]string{"cache/index.cache.json": "{}", "fetch-cache/abc": "x"})

	if _, err := p.ProvisionFunctions(context.Background(), builtAt(routedNextSpec(), root), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	uploads := server.Stored()
	names := uploadNames(uploads)
	slices.Sort(names)
	want := []string{"cache/shop/web/prod/r1/isr/cache/index.cache.json", "cache/shop/web/prod/r1/isr/fetch-cache/abc"}
	if !slices.Equal(names, want) {
		t.Errorf("the deploy uploaded %v, want the two entries and no tag file", names)
	}
	for _, u := range uploads {
		if u.IfGenerationMatch != "0" {
			t.Errorf("%s was written with ifGenerationMatch=%q, want 0 so a redeploy keeps what is there", u.Name, u.IfGenerationMatch)
		}
	}
	events := server.Happened()
	if events[len(events)-1] != "create" {
		t.Errorf("the deploy did %v, want every upload before the service is created", events)
	}
}

func TestARedeployOfTheSameReleaseKeepsTheEntriesItHas(t *testing.T) {
	server := &googletest.Run{Present: map[string]bool{
		"cache/shop/web/prod/r1/isr/cache/index.cache.json": true,
		"cache/shop/web/prod/r1/isr/fetch-cache/abc":        true,
	}}
	p := providerServing(server, t)
	root := plantPrerenders(t, map[string]string{"cache/index.cache.json": "{}", "fetch-cache/abc": "x"})

	if _, err := p.ProvisionFunctions(context.Background(), builtAt(routedNextSpec(), root), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	names := uploadNames(server.Stored())
	slices.Sort(names)
	if len(names) != 2 || len(slices.Compact(slices.Clone(names))) != 2 {
		t.Errorf("the redeploy attempted %v, want one attempt for each of the two objects", names)
	}
}

func TestANextAppWithoutAnIncrementalCacheSeedsNothing(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	root := plantPrerenders(t, map[string]string{"cache/index.cache.json": "{}"})

	if _, err := p.ProvisionFunctions(context.Background(), builtAt(nextSpec(), root), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	if got := server.Stored(); len(got) != 0 {
		t.Errorf("an app with no incremental cache uploaded %v, want nothing", uploadNames(got))
	}
}

func TestANodeAppSeedsNothing(t *testing.T) {
	spec := nextSpec()
	spec.App.Framework = buildoutput.FrameworkNode
	spec.App.Functions[0].Framework.Name = buildoutput.FrameworkNode
	spec.App.ISR = &provider.ISRSpec{Prefix: "prod/shop/web/r1/isr"}
	server := &googletest.Run{}
	p := providerServing(server, t)
	root := plantPrerenders(t, map[string]string{"cache/index.cache.json": "{}"})

	if _, err := p.ProvisionFunctions(context.Background(), builtAt(spec, root), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	if got := server.Stored(); len(got) != 0 {
		t.Errorf("a node app uploaded %v, want nothing", uploadNames(got))
	}
}

func builtAt(spec provider.StackSpec, root string) provider.StackSpec {
	spec.App.OutputRoot = root
	return spec
}
