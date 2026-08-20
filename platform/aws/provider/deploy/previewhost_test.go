package deploy

import (
	"strings"
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func TestPreviewHost(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		pointer   string
		app       string
		base      string
		singleApp bool
		want      string
	}{
		{"single app elides the app label", "pr-42-a1b2c3d4", "web", "preview.acme.com", true, "pr-42-a1b2c3d4.preview.acme.com"},
		{"multi app qualifies the label", "pr-42-a1b2c3d4", "web", "preview.acme.com", false, "pr-42-a1b2c3d4--web.preview.acme.com"},
		{"no pointer", "", "web", "preview.acme.com", true, ""},
		{"no base", "pr-42-a1b2c3d4", "web", "", true, ""},
		{"neither", "", "web", "", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := previewHost(tc.pointer, tc.app, tc.base, tc.singleApp); got != tc.want {
				t.Errorf("previewHost(%q, %q, %q, %v) = %q, want %q", tc.pointer, tc.app, tc.base, tc.singleApp, got, tc.want)
			}
		})
	}

	t.Run("the separator survives hyphenated halves", func(t *testing.T) {
		t.Parallel()

		host := previewHost("feat-a-b", "my-app", "preview.acme.com", false)
		label, _, _ := strings.Cut(host, ".")
		pointer, app, ok := strings.Cut(label, previewAppSeparator)
		if !ok || pointer != "feat-a-b" || app != "my-app" {
			t.Errorf("label %q splits to (%q, %q, %v), want (feat-a-b, my-app, true)", label, pointer, app, ok)
		}
	})

	t.Run("a max pointer fills the label", func(t *testing.T) {
		t.Parallel()

		pointer := strings.Repeat("p", previewLabelMaxLen)
		label, _, _ := strings.Cut(previewHost(pointer, "web", "preview.acme.com", true), ".")
		if len(label) != previewLabelMaxLen {
			t.Errorf("label %q is %d chars, want %d", label, len(label), previewLabelMaxLen)
		}
	})
}

func TestPreviewBaseDomain(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"a wildcard over a preview subdomain", "*.preview.acme.com", "preview.acme.com"},
		{"a wildcard over the apex", "*.acme.com", "acme.com"},
		{"a plain hostname has no base", "acme.com", ""},
		{"nothing declared", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := previewBaseDomain(tc.in); got != tc.want {
				t.Errorf("previewBaseDomain(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestPreviewWildcard(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"a base domain gains the wildcard label", "preview.acme.com", "*.preview.acme.com"},
		{"nothing declared", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := previewWildcard(tc.in); got != tc.want {
				t.Errorf("previewWildcard(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestPreviewHostnames(t *testing.T) {
	t.Parallel()

	t.Run("resolves the wildcard to the pointer hosts, a single app elides the app label", func(t *testing.T) {
		t.Parallel()
		cfg := Config{Class: deploymentsv1.Environment_CLASS_PREVIEW, Slug: "proj", Identity: "pr-42"}
		apps := []*deploymentsv1.ManifestApp{{Name: "web"}}
		got, err := previewHostnames(cfg, apps, map[string][]string{"web": {"*.preview.acme.com"}})
		if err != nil {
			t.Fatalf("previewHostnames: %v", err)
		}
		if want := []string{"pr-42.preview.acme.com"}; !slicesEqual(got.hosts["web"], want) {
			t.Errorf("hosts = %v, want %v", got.hosts["web"], want)
		}
		if got.previewBase != "preview.acme.com" {
			t.Errorf("previewBase = %q, want preview.acme.com", got.previewBase)
		}
	})

	t.Run("resolves the wildcard to the pointer hosts, two apps qualify the label", func(t *testing.T) {
		t.Parallel()
		cfg := Config{Class: deploymentsv1.Environment_CLASS_PREVIEW, Slug: "proj", Identity: "pr-42"}
		apps := []*deploymentsv1.ManifestApp{{Name: "web"}, {Name: "api"}}
		declared := map[string][]string{"web": {"*.preview.acme.com"}, "api": {"*.preview.acme.com"}}
		got, err := previewHostnames(cfg, apps, declared)
		if err != nil {
			t.Fatalf("previewHostnames: %v", err)
		}
		if want := []string{"pr-42--web.preview.acme.com"}; !slicesEqual(got.hosts["web"], want) {
			t.Errorf("web hosts = %v, want %v", got.hosts["web"], want)
		}
		if want := []string{"pr-42--api.preview.acme.com"}; !slicesEqual(got.hosts["api"], want) {
			t.Errorf("api hosts = %v, want %v", got.hosts["api"], want)
		}
	})

	t.Run("serves an app that declares nothing under the project wildcard", func(t *testing.T) {
		t.Parallel()
		cfg := Config{Class: deploymentsv1.Environment_CLASS_PREVIEW, Slug: "proj", Identity: "pr-42"}
		apps := []*deploymentsv1.ManifestApp{{Name: "web"}, {Name: "api"}}

		got, err := previewHostnames(cfg, apps, map[string][]string{"web": {"*.preview.acme.com"}})
		if err != nil {
			t.Fatalf("previewHostnames: %v", err)
		}
		if want := []string{"pr-42--api.preview.acme.com"}; !slicesEqual(got.hosts["api"], want) {
			t.Errorf("api hosts = %v, want %v", got.hosts["api"], want)
		}
	})

	t.Run("two preview domains in one project are refused", func(t *testing.T) {
		t.Parallel()
		cfg := Config{Class: deploymentsv1.Environment_CLASS_PREVIEW, Slug: "proj", Identity: "pr-42"}
		apps := []*deploymentsv1.ManifestApp{{Name: "web"}, {Name: "api"}}
		declared := map[string][]string{"web": {"*.preview.acme.com"}, "api": {"*.preview.other.com"}}

		_, err := previewHostnames(cfg, apps, declared)
		if err == nil {
			t.Fatal("expected two project preview domains to be refused")
		}
		if !strings.Contains(err.Error(), "*.preview.acme.com") || !strings.Contains(err.Error(), "*.preview.other.com") {
			t.Errorf("error must name both domains, got %q", err)
		}
	})

}

func TestResolveWorkerHostnames(t *testing.T) {
	t.Parallel()

	t.Run("production serves its declared hostnames", func(t *testing.T) {
		t.Parallel()
		manifest := &deploymentsv1.Manifest{
			Slug:      "proj",
			Apps:      []*deploymentsv1.ManifestApp{{Name: "web", Framework: "next"}},
			Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "web_index", Framework: "next", App: "web", RouteId: "/"}},
			Domains:   map[string]*deploymentsv1.DomainList{"production": {Hostnames: []string{"acme.com", "www.acme.com"}}},
		}
		artifactRoot := t.TempDir()
		writeRoutingManifest(t, artifactRoot, "web", `{"buildId":"b1"}`)
		cfg := Config{Class: deploymentsv1.Environment_CLASS_PRODUCTION, Slug: "proj", ArtifactRoot: artifactRoot}

		apps := workerApps(artifactRoot, manifest)
		resolved, err := hostingWorldFor(cfg, manifest).hostnames(cfg, manifest, apps)
		if err != nil {
			t.Fatalf("hostnames: %v", err)
		}
		if want := []string{"acme.com", "www.acme.com"}; !slicesEqual(resolved.hosts["web"], want) {
			t.Errorf("hostnames = %v, want the declared hostnames %v", resolved.hosts["web"], want)
		}
	})
}
