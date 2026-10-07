package project

import (
	"strings"
	"testing"
)

func TestAnAppIsBuiltWithBindingsUnlessItsBuildSaysOtherwise(t *testing.T) {
	t.Parallel()

	cfg := mustLoadJSON(t, `{"slug":"shop","apps":[
		{"name":"web","path":"web","compute":"serverless","framework":"next","build":{"bindings":false}},
		{"name":"site","path":"site","compute":"container","build":{"bindings":false,"command":"make"}},
		{"name":"admin","path":"admin","compute":"serverless","framework":"next"}
	]}`)

	for _, app := range cfg.Apps {
		if want := app.Name == "admin"; app.BuildsWithBindings != want {
			t.Errorf("app %s builds with bindings = %v, want %v", app.Name, app.BuildsWithBindings, want)
		}
	}
	if web := cfg.Apps[0]; web.Container != nil {
		t.Errorf("web = %+v, want the serverless shape alone: build.bindings configures no image", web)
	}
	if site := cfg.Apps[1]; site.Container == nil || site.Container.Build == nil || site.Container.Build.Command != "make" {
		t.Errorf("site = %+v, want its image build kept beside build.bindings", site)
	}
}

func TestAServerlessAppSettingAnImageBuildBesideBindingsIsStillRefused(t *testing.T) {
	t.Parallel()

	_, err := loadJSON(t, `{"slug":"shop","apps":[{"name":"web","path":"web","compute":"serverless","framework":"next","build":{"bindings":false,"dockerfile":"Dockerfile"}}]}`)
	if err == nil || !strings.Contains(err.Error(), "`build`") {
		t.Fatalf("Load err = %v, want the image build refused on an app that runs as functions", err)
	}
}
