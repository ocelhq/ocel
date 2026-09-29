package appurl_test

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/appurl"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	"github.com/ocelhq/ocel/pkg/appbuild"
	"github.com/ocelhq/ocel/pkg/constants"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func TestProduction(t *testing.T) {
	t.Parallel()

	t.Run("an unnamed app takes the project's first production hostname", func(t *testing.T) {
		t.Parallel()
		cfg := &project.Project{Domains: map[string][]string{"production": {"acme.com", "www.acme.com"}}}

		if got, want := appurl.Production(cfg)[variablescope.RootApp], "https://acme.com"; got != want {
			t.Errorf("url = %q, want %q", got, want)
		}
	})

	t.Run("an app's own domain wins over the project's", func(t *testing.T) {
		t.Parallel()
		cfg := &project.Project{
			Domains: map[string][]string{"production": {"acme.com"}},
			Apps: []project.App{
				{Name: "web"},
				{Name: "api", Domains: map[string][]string{"production": {"api.acme.com", "api2.acme.com"}}},
			},
		}

		urls := appurl.Production(cfg)
		if got, want := urls["web"], "https://acme.com"; got != want {
			t.Errorf("web url = %q, want the project's %q", got, want)
		}
		if got, want := urls["api"], "https://api.acme.com"; got != want {
			t.Errorf("api url = %q, want its own first %q", got, want)
		}
	})

	t.Run("an app with nothing declared is given no url at all", func(t *testing.T) {
		t.Parallel()
		cfg := &project.Project{Apps: []project.App{{Name: "web"}}}

		if urls := appurl.Production(cfg); len(urls) != 0 {
			t.Errorf("urls = %v, want none: a project that declares no production domain has no hostname to hand out", urls)
		}
	})

	t.Run("hands a project-level domain to the app the deploy serves it on, and no other", func(t *testing.T) {
		t.Parallel()
		cfg := &project.Project{
			Domains: map[string][]string{"production": {"acme.com"}},
			Apps:    []project.App{{Name: "web"}, {Name: "api"}},
		}

		served := appbuild.AttributeHostnames(cfg.Domains["production"], [][]string{nil, nil})
		urls := appurl.Production(cfg)
		if got, want := urls["web"], "https://"+served[0][0]; got != want {
			t.Errorf("web url = %q, want %q: the deploy serves a project hostname on the first app `apps` names", got, want)
		}
		if got, ok := urls["api"]; ok {
			t.Errorf("api url = %q, want none: nothing serves api, so an url would name a host that answers for web", got)
		}
	})
}

func TestPreview(t *testing.T) {
	t.Parallel()

	t.Run("one app is served on the preview's single hostname", func(t *testing.T) {
		t.Parallel()
		cfg := &project.Project{Apps: []project.App{{Name: "web"}}}

		urls := appurl.Preview(cfg, func(app string) string {
			if app != "" {
				t.Errorf("host(%q), want the unlabelled host where one app is served", app)
			}
			return "pr-1.preview.acme.com"
		})
		if got, want := urls["web"], "https://pr-1.preview.acme.com"; got != want {
			t.Errorf("web url = %q, want %q", got, want)
		}
	})

	t.Run("two apps are each served on their own labelled hostname", func(t *testing.T) {
		t.Parallel()
		cfg := &project.Project{Apps: []project.App{{Name: "web"}, {Name: "api"}}}

		urls := appurl.Preview(cfg, func(app string) string { return "pr-1--" + app + ".preview.acme.com" })
		if got, want := urls["api"], "https://pr-1--api.preview.acme.com"; got != want {
			t.Errorf("api url = %q, want %q", got, want)
		}
	})
}

func TestPrepend(t *testing.T) {
	t.Parallel()

	cfg := &project.Project{Apps: []project.App{
		{Name: "web", Framework: project.Framework{Name: appbuild.FrameworkNext}},
		{Name: "api", Framework: project.Framework{Name: appbuild.FrameworkGo}},
		{Name: "docs"},
	}}
	byApp := map[string][]variables.Variable{
		"web":  {{Key: "LOG_LEVEL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "info"}},
		"api":  nil,
		"docs": nil,
	}
	appurl.Prepend(cfg, byApp, map[string]string{"web": "https://acme.com", "api": "https://api.acme.com"})

	variables := map[string]variables.Variable{}
	for _, v := range byApp["web"] {
		variables[v.Key] = v
	}
	if got, want := variables[constants.AppURLEnvName].Value, "https://acme.com"; got != want {
		t.Errorf("%s = %q, want %q", constants.AppURLEnvName, got, want)
	}
	if got, want := variables[appbuild.ClientURLEnvName].Value, "https://acme.com"; got != want {
		t.Errorf("%s = %q, want the same value mirrored for the browser bundle", appbuild.ClientURLEnvName, got)
	}
	if !variables[appbuild.ClientURLEnvName].ClientAccessible {
		t.Errorf("%s is not client-accessible, so nothing would inline it into the bundle", appbuild.ClientURLEnvName)
	}
	if variables[constants.AppURLEnvName].ClientAccessible {
		t.Errorf("%s is client-accessible, and a bundler inlines only its own public prefix", constants.AppURLEnvName)
	}
	if variables["LOG_LEVEL"].Value != "info" {
		t.Errorf("web variables = %+v, want the declared ones kept", byApp["web"])
	}
	if got := keys(byApp["api"]); !slices.Equal(got, []string{constants.AppURLEnvName}) {
		t.Errorf("api variables = %v, want only %s: a go app has no bundle to read %s, and a value of its own under that name would be overwritten", got, constants.AppURLEnvName, appbuild.ClientURLEnvName)
	}
	if len(byApp["docs"]) != 0 {
		t.Errorf("docs variables = %+v, want none where the app has no hostname", byApp["docs"])
	}
}

func keys(variables []variables.Variable) []string {
	out := make([]string, 0, len(variables))
	for _, v := range variables {
		out = append(out, v.Key)
	}
	return out
}
