package gcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/prerender"
)

const seededSnapshot = "cache/shop/web/prod/r1/isr/tag-clock.json"

func plantPrerenders(t *testing.T, p *Provider, files map[string]string) {
	t.Helper()
	root, err := buildoutput.Root(p.projectDir)
	if err != nil {
		t.Fatal(err)
	}
	for rel, body := range files {
		path := filepath.Join(buildoutput.AppRoot(root, "web"), filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func uploadNames(uploads []upload) []string {
	names := make([]string, 0, len(uploads))
	for _, u := range uploads {
		names = append(names, u.name)
	}
	return names
}

func TestANextDeploySeedsUnderTheCacheStoreProjectFirst(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	plantPrerenders(t, p, map[string]string{"cache/index.cache.json": "{}", "fetch-cache/abc": "x"})

	if _, err := p.ProvisionFunctions(context.Background(), routedNextSpec(), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	uploads := server.stored()
	if len(uploads) != 3 {
		t.Fatalf("the deploy uploaded %v, want the tag snapshot and two entries", uploadNames(uploads))
	}
	if uploads[0].name != seededSnapshot {
		t.Errorf("the first upload is %q, want the genesis tag snapshot %q", uploads[0].name, seededSnapshot)
	}
	var snapshot prerender.TagSnapshot
	if err := json.Unmarshal(uploads[0].body, &snapshot); err != nil {
		t.Fatalf("the tag snapshot is not JSON: %v", err)
	}
	if snapshot.Version != prerender.TagSnapshotVersion || len(snapshot.Records) != 0 || snapshot.DeployedAt != snapshot.GeneratedAt {
		t.Errorf("the tag snapshot = %+v, want the genesis one", snapshot)
	}
	rest := uploadNames(uploads[1:])
	slices.Sort(rest)
	want := []string{"cache/shop/web/prod/r1/isr/cache/index.cache.json", "cache/shop/web/prod/r1/isr/fetch-cache/abc"}
	if !slices.Equal(rest, want) {
		t.Errorf("the entries landed at %v, want %v", rest, want)
	}
	for _, u := range uploads {
		if u.ifGenerationMatch != "0" {
			t.Errorf("%s was written with ifGenerationMatch=%q, want 0 so a redeploy keeps what is there", u.name, u.ifGenerationMatch)
		}
	}
	events := server.happened()
	if events[len(events)-1] != "create" {
		t.Errorf("the deploy did %v, want every upload before the service is created", events)
	}
}

func TestARedeployOfTheSameReleaseKeepsTheTagSnapshotAndEntriesItHas(t *testing.T) {
	server := &runServer{present: map[string]bool{
		seededSnapshot: true,
		"cache/shop/web/prod/r1/isr/cache/index.cache.json": true,
		"cache/shop/web/prod/r1/isr/fetch-cache/abc":        true,
	}}
	p := server.open(t)
	plantPrerenders(t, p, map[string]string{"cache/index.cache.json": "{}", "fetch-cache/abc": "x"})

	if _, err := p.ProvisionFunctions(context.Background(), routedNextSpec(), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	names := uploadNames(server.stored())
	slices.Sort(names)
	if len(names) != 3 || len(slices.Compact(slices.Clone(names))) != 3 {
		t.Errorf("the redeploy attempted %v, want one attempt for each of the three objects", names)
	}
}

func TestANextAppWithoutAnIncrementalCacheSeedsNothing(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	plantPrerenders(t, p, map[string]string{"cache/index.cache.json": "{}"})

	if _, err := p.ProvisionFunctions(context.Background(), nextSpec(), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	if got := server.stored(); len(got) != 0 {
		t.Errorf("an app with no incremental cache uploaded %v, want nothing", uploadNames(got))
	}
}

func TestANodeAppSeedsNothing(t *testing.T) {
	spec := nextSpec()
	spec.App.Framework = buildoutput.FrameworkNode
	spec.App.Functions[0].Framework.Name = buildoutput.FrameworkNode
	spec.App.ISR = &provider.ISRSpec{Prefix: "prod/shop/web/r1/isr"}
	server := &runServer{}
	p := server.open(t)
	plantPrerenders(t, p, map[string]string{"cache/index.cache.json": "{}"})

	if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	if got := server.stored(); len(got) != 0 {
		t.Errorf("a node app uploaded %v, want nothing", uploadNames(got))
	}
}
