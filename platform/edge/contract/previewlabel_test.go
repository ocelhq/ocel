package edge

import (
	"strings"
	"testing"
)

func TestPreviewSiteHost(t *testing.T) {
	t.Parallel()

	shared := SharedPreview("shop", "preview.acme.com")
	if got := shared.Host("pr-12", ""); got != "shop--pr-12.preview.acme.com" {
		t.Errorf("got %q, want the project named on a wildcard every project shares", got)
	}
	if got := shared.Host("pr-12", "web"); got != "shop--pr-12--web.preview.acme.com" {
		t.Errorf("got %q, want the app qualified", got)
	}

	own := ProjectPreview("preview.acme.com")
	if got := own.Host("pr-12", ""); got != "pr-12.preview.acme.com" {
		t.Errorf("got %q, want no project segment: the wildcard is this project's own", got)
	}
	if got := own.Host("pr-12", "web"); got != "pr-12--web.preview.acme.com" {
		t.Errorf("got %q, want the app qualified without the project", got)
	}

	for name, site := range map[string]PreviewSite{
		"a shared wildcard": shared,
		"a project's own":   own,
		"no wildcard":       ProjectPreview(""),
	} {
		if got := site.Host("", ""); got != "" {
			t.Errorf("%s with no pointer = %q, want nothing", name, got)
		}
	}
	if got := SharedPreview("shop", "").Host("pr-12", ""); got != "" {
		t.Errorf("no base domain = %q, want nothing: a bare domain is not a preview hostname", got)
	}
}

func TestPreviewSiteHosts(t *testing.T) {
	t.Parallel()

	site := SharedPreview("shop", "preview.acme.com")
	if got := site.Hosts("pr-12", nil); len(got) != 1 || got[0] != "shop--pr-12.preview.acme.com" {
		t.Errorf("got %v, want the app-less hostname a single-app project serves", got)
	}
	if got := site.Hosts("pr-12", []string{"web"}); len(got) != 1 || got[0] != "shop--pr-12.preview.acme.com" {
		t.Errorf("got %v, want the app-less hostname a single-app project serves", got)
	}
	want := []string{"shop--pr-12--web.preview.acme.com", "shop--pr-12--api.preview.acme.com"}
	got := site.Hosts("pr-12", []string{"web", "api"})
	if len(got) != len(want) {
		t.Fatalf("got %v, want one hostname per app: %v", got, want)
	}
	for i, host := range want {
		if got[i] != host {
			t.Errorf("hostname %d = %q, want %q", i, got[i], host)
		}
	}
	if got := site.Hosts("", []string{"web", "api"}); len(got) != 0 {
		t.Errorf("got %v, want nothing without a pointer", got)
	}
}

func TestPreviewSiteLabelProblem(t *testing.T) {
	t.Parallel()

	pointer := strings.Repeat("b", 60)
	shared := SharedPreview("shop", "preview.acme.com")
	err := shared.LabelProblem(shared.Hosts(pointer, nil))
	if err == nil {
		t.Fatal("LabelProblem err = nil, want the refusal DNS would raise")
	}
	for _, want := range []string{"66 characters", "63", `project "shop"`, `preview "` + pointer} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("LabelProblem = %q, want it to name %s", err, want)
		}
	}

	own := ProjectPreview("preview.acme.com")
	if err := own.LabelProblem(own.Hosts(pointer, nil)); err != nil {
		t.Errorf("LabelProblem = %v, want nil: the project segment the shared wildcard adds is not served here", err)
	}
	if err := own.LabelProblem([]string{"*.preview.acme.com"}); err != nil {
		t.Errorf("LabelProblem = %v, want nil: a wildcard is not a label a deploy serves", err)
	}
}
