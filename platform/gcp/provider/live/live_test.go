package live

import (
	"strings"
	"testing"

	rt "github.com/ocelhq/ocel/pkg/runtimekit/live"
)

func complete() Manifest {
	return Manifest{
		Project: "acme-prod", Region: "europe-west1", Namespace: "ocel", Slug: "shop", Class: "production",
		Keys: []rt.Key{{Key: "DATABASE_URL"}},
	}
}

func TestAManifestNamingNothingLiveRendersToNothing(t *testing.T) {
	t.Parallel()
	held := complete()
	held.Keys = nil
	rendered, err := Render(held)
	if err != nil || rendered != nil {
		t.Errorf("Render() = %q, %v, want nothing: a container with no live value boots with no manifest and opens no store", rendered, err)
	}
}

func TestARenderedManifestParsesBackToWhatWasPinned(t *testing.T) {
	t.Parallel()
	held := complete()
	held.Class, held.Environment = "preview", "pr-7"
	rendered, err := Render(held)
	if err != nil {
		t.Fatalf("Render() = %v", err)
	}
	parsed, err := Parse(rendered)
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	if parsed.Project != held.Project || parsed.Region != held.Region || parsed.Namespace != held.Namespace ||
		parsed.Slug != held.Slug || parsed.Class != held.Class || parsed.Environment != held.Environment ||
		len(parsed.Keys) != 1 || parsed.Keys[0].Key != "DATABASE_URL" {
		t.Errorf("Parse(Render()) = %+v, want %+v", parsed, held)
	}
	if strings.Contains(string(rendered), "endpoint") {
		t.Errorf("a manifest against the real project carries %s, and an endpoint there would send the runtime past Google", rendered)
	}
}

func TestAManifestMissingWhatAddressesTheStoreIsRefused(t *testing.T) {
	t.Parallel()
	for name, sabotage := range map[string]func(*Manifest){
		"project":                   func(m *Manifest) { m.Project = "" },
		"region":                    func(m *Manifest) { m.Region = "" },
		"namespace":                 func(m *Manifest) { m.Namespace = "" },
		"slug":                      func(m *Manifest) { m.Slug = "" },
		"class":                     func(m *Manifest) { m.Class = "" },
		"a class nothing is called": func(m *Manifest) { m.Class = "staging" },
	} {
		t.Run(name, func(t *testing.T) {
			held := complete()
			sabotage(&held)
			if _, err := Render(held); err == nil {
				t.Errorf("Render() without %s = nil, want a refusal: the runtime would boot with nowhere to read from", name)
			}
		})
	}
}
