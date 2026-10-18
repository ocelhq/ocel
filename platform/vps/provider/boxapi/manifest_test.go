package boxapi

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/runtime/live"
)

func complete() Manifest {
	return Manifest{Slug: "shop", Tier: "production", Keys: []live.Key{{Key: "DATABASE_URL"}}}
}

func TestAManifestNamingNothingLiveRendersToNothing(t *testing.T) {
	t.Parallel()
	manifest := complete()
	manifest.Keys = nil
	rendered, err := Render(manifest)
	if err != nil || rendered != nil {
		t.Errorf("Render() = %q, %v, want nothing: a container with no live value boots with no manifest and dials no socket", rendered, err)
	}
}

func TestARenderedManifestParsesBackToWhatWasPinned(t *testing.T) {
	t.Parallel()
	manifest := complete()
	manifest.Tier, manifest.Environment = "preview", "pr-7"
	manifest.Keys = append(manifest.Keys, live.Key{Key: "SESSION", Folder: "/web"})
	rendered, err := Render(manifest)
	if err != nil {
		t.Fatalf("Render() = %v", err)
	}
	parsed, err := Parse(rendered)
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	if parsed.Slug != "shop" || parsed.Tier != "preview" || parsed.Environment != "pr-7" ||
		len(parsed.Keys) != 2 || parsed.Keys[1].Folder != "/web" {
		t.Errorf("Parse(Render()) = %+v, want %+v", parsed, manifest)
	}
}

func TestAManifestMissingWhatScopesTheStoreIsRefused(t *testing.T) {
	t.Parallel()
	for name, sabotage := range map[string]func(*Manifest){
		"slug":                     func(m *Manifest) { m.Slug = "" },
		"tier":                     func(m *Manifest) { m.Tier = "" },
		"a tier nothing is called": func(m *Manifest) { m.Tier = "staging" },
	} {
		t.Run(name, func(t *testing.T) {
			manifest := complete()
			sabotage(&manifest)
			if _, err := Render(manifest); err == nil {
				t.Errorf("Render() without %s = nil, want a refusal: the agent would look for the value under no scope at all", name)
			}
		})
	}
}
