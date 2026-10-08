package project

import (
	"testing"
)

func TestAnAppIsBuiltWithItsResourcesUnlessItSaysOtherwise(t *testing.T) {
	t.Parallel()

	cfg := mustLoadJSON(t, `{"slug":"shop","apps":[
		{"name":"web","path":"web","compute":{"serverless":{"framework":"next"}},"buildWithResources":false},
		{"name":"site","path":"site","compute":{"container":{"image":{"command":"make"}}},"buildWithResources":false},
		{"name":"admin","path":"admin","compute":{"serverless":{"framework":"next"}}}
	]}`)

	for _, app := range cfg.Apps {
		if want := app.Name == "admin"; app.BuildsWithResources != want {
			t.Errorf("app %s builds with resources = %v, want %v", app.Name, app.BuildsWithResources, want)
		}
	}
	if site := cfg.Apps[1]; site.Container == nil || site.Container.Build == nil || site.Container.Build.Command != "make" {
		t.Errorf("site = %+v, want its image build kept beside buildWithResources", site)
	}
}
