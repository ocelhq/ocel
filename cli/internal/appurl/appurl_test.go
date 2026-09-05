package appurl_test

import (
	"testing"

	"github.com/ocelhq/ocel/cli/internal/appurl"
	"github.com/ocelhq/ocel/cli/internal/envwire"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
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

func TestAdd(t *testing.T) {
	t.Parallel()

	byApp := map[string][]manifestbuilder.Variable{
		"web": {{Key: "LOG_LEVEL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "info"}},
		"api": nil,
	}
	appurl.Add(byApp, map[string]string{"web": "https://acme.com"})

	held := map[string]manifestbuilder.Variable{}
	for _, v := range byApp["web"] {
		held[v.Key] = v
	}
	if got, want := held[appurl.Name].Value, "https://acme.com"; got != want {
		t.Errorf("%s = %q, want %q", appurl.Name, got, want)
	}
	if got, want := held[appurl.ClientName].Value, "https://acme.com"; got != want {
		t.Errorf("%s = %q, want the same value mirrored for the browser bundle", appurl.ClientName, got)
	}
	if !held[appurl.ClientName].ClientAccessible {
		t.Errorf("%s is not client-accessible, so nothing would inline it into the bundle", appurl.ClientName)
	}
	if held[appurl.Name].ClientAccessible {
		t.Errorf("%s is client-accessible, and a bundler inlines only its own public prefix", appurl.Name)
	}
	if held["LOG_LEVEL"].Value != "info" {
		t.Errorf("web variables = %+v, want the declared ones kept", byApp["web"])
	}
	if len(byApp["api"]) != 0 {
		t.Errorf("api variables = %+v, want none where the app has no hostname", byApp["api"])
	}
}

func TestBuildEnv(t *testing.T) {
	t.Parallel()

	env := appurl.BuildEnv(map[string]string{envwire.RootApp: "https://acme.com"})
	if got, want := env[""][appurl.Name], "https://acme.com"; got != want {
		t.Errorf("build env = %v, want the unnamed app keyed as the builder keys it, holding %q", env, want)
	}
	if got, want := env[""][appurl.ClientName], "https://acme.com"; got != want {
		t.Errorf("build env %s = %q, want %q", appurl.ClientName, got, want)
	}
}
