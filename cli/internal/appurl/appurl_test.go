package appurl_test

import (
	"maps"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/appurl"
	"github.com/ocelhq/ocel/cli/internal/envwire"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/pkg/constants"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestProduction(t *testing.T) {
	t.Parallel()

	t.Run("an unnamed app takes the project's first production hostname", func(t *testing.T) {
		t.Parallel()
		cfg := &projectconfig.Config{Domains: map[string][]string{"production": {"acme.com", "www.acme.com"}}}

		if got, want := appurl.Production(cfg)[envwire.RootApp], "https://acme.com"; got != want {
			t.Errorf("url = %q, want %q", got, want)
		}
	})

	t.Run("an app's own domain wins over the project's", func(t *testing.T) {
		t.Parallel()
		cfg := &projectconfig.Config{
			Domains: map[string][]string{"production": {"acme.com"}},
			Apps: []projectconfig.App{
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
		cfg := &projectconfig.Config{Apps: []projectconfig.App{{Name: "web"}}}

		if urls := appurl.Production(cfg); len(urls) != 0 {
			t.Errorf("urls = %v, want none: a project that declares no production domain has no hostname to hand out", urls)
		}
	})

	t.Run("hands a project-level domain to the app the deploy serves it on, and no other", func(t *testing.T) {
		t.Parallel()
		cfg := &projectconfig.Config{
			Domains: map[string][]string{"production": {"acme.com"}},
			Apps:    []projectconfig.App{{Name: "web"}, {Name: "api"}},
		}

		served := providerkit.AttributeHostnames(cfg.Domains["production"], [][]string{nil, nil})
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
		cfg := &projectconfig.Config{Apps: []projectconfig.App{{Name: "web"}}}

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
		cfg := &projectconfig.Config{Apps: []projectconfig.App{{Name: "web"}, {Name: "api"}}}

		urls := appurl.Preview(cfg, func(app string) string { return "pr-1--" + app + ".preview.acme.com" })
		if got, want := urls["api"], "https://pr-1--api.preview.acme.com"; got != want {
			t.Errorf("api url = %q, want %q", got, want)
		}
	})
}

func TestPrepend(t *testing.T) {
	t.Parallel()

	cfg := &projectconfig.Config{Apps: []projectconfig.App{
		{Name: "web", Runtime: projectconfig.Runtime{Name: providerkit.RuntimeNext}},
		{Name: "api", Runtime: projectconfig.Runtime{Name: providerkit.RuntimeGo}},
		{Name: "docs"},
	}}
	byApp := map[string][]manifestbuilder.Variable{
		"web":  {{Key: "LOG_LEVEL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "info"}},
		"api":  nil,
		"docs": nil,
	}
	appurl.Prepend(cfg, byApp, map[string]string{"web": "https://acme.com", "api": "https://api.acme.com"})

	held := map[string]manifestbuilder.Variable{}
	for _, v := range byApp["web"] {
		held[v.Key] = v
	}
	if got, want := held[constants.AppURLEnvName].Value, "https://acme.com"; got != want {
		t.Errorf("%s = %q, want %q", constants.AppURLEnvName, got, want)
	}
	if got, want := held[providerkit.ClientURLEnvName].Value, "https://acme.com"; got != want {
		t.Errorf("%s = %q, want the same value mirrored for the browser bundle", providerkit.ClientURLEnvName, got)
	}
	if !held[providerkit.ClientURLEnvName].ClientAccessible {
		t.Errorf("%s is not client-accessible, so nothing would inline it into the bundle", providerkit.ClientURLEnvName)
	}
	if held[constants.AppURLEnvName].ClientAccessible {
		t.Errorf("%s is client-accessible, and a bundler inlines only its own public prefix", constants.AppURLEnvName)
	}
	if held["LOG_LEVEL"].Value != "info" {
		t.Errorf("web variables = %+v, want the declared ones kept", byApp["web"])
	}
	if got := keys(byApp["api"]); !slices.Equal(got, []string{constants.AppURLEnvName}) {
		t.Errorf("api variables = %v, want only %s: a go app has no bundle to read %s, and a value of its own under that name would be overwritten", got, constants.AppURLEnvName, providerkit.ClientURLEnvName)
	}
	if len(byApp["docs"]) != 0 {
		t.Errorf("docs variables = %+v, want none where the app has no hostname", byApp["docs"])
	}
}

func TestBuildEnv(t *testing.T) {
	t.Parallel()

	env := appurl.BuildEnv(&projectconfig.Config{}, map[string]string{envwire.RootApp: "https://acme.com"})
	if got, want := env[""][constants.AppURLEnvName], "https://acme.com"; got != want {
		t.Errorf("build env = %v, want the unnamed app keyed as the builder keys it, holding %q", env, want)
	}
	if got, want := env[""][providerkit.ClientURLEnvName], "https://acme.com"; got != want {
		t.Errorf("build env %s = %q, want %q: an app `apps` does not name is built by the node builder", providerkit.ClientURLEnvName, got, want)
	}

	cfg := &projectconfig.Config{Apps: []projectconfig.App{{Name: "api", Runtime: projectconfig.Runtime{Name: providerkit.RuntimePython}}}}
	if got := appurl.BuildEnv(cfg, map[string]string{"api": "https://api.acme.com"})["api"]; !maps.Equal(got, map[string]string{constants.AppURLEnvName: "https://api.acme.com"}) {
		t.Errorf("build env = %v, want only %s for a python app", got, constants.AppURLEnvName)
	}

	if got := appurl.BuildEnv(cfg, map[string]string{"api": ""})["api"]; len(got) != 0 {
		t.Errorf("build env = %v, want the key absent where the app is served on no hostname, rather than an empty string a build would parse", got)
	}
}

func keys(variables []manifestbuilder.Variable) []string {
	out := make([]string, 0, len(variables))
	for _, v := range variables {
		out = append(out, v.Key)
	}
	return out
}
