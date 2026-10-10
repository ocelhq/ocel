package appurl_test

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/appurl"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/processenv"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func TestProduction(t *testing.T) {
	t.Parallel()

	t.Run("a project's only app takes the project's first production hostname", func(t *testing.T) {
		t.Parallel()
		cfg := &project.Project{Apps: []project.App{{Name: "shop"}}, Domains: project.Domains{Production: []string{"acme.com", "www.acme.com"}}}

		if got, want := appurl.FormatProductionURLs(cfg)["shop"], "https://acme.com"; got != want {
			t.Errorf("url = %q, want %q", got, want)
		}
	})

	t.Run("an app's own domain wins over the project's", func(t *testing.T) {
		t.Parallel()
		cfg := &project.Project{
			Domains: project.Domains{Production: []string{"acme.com"}},
			Apps: []project.App{
				{Name: "web"},
				{Name: "api", ProductionDomains: []string{"api.acme.com", "api2.acme.com"}},
			},
		}

		urls := appurl.FormatProductionURLs(cfg)
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

		if urls := appurl.FormatProductionURLs(cfg); len(urls) != 0 {
			t.Errorf("urls = %v, want none: a project that declares no production domain has no hostname to hand out", urls)
		}
	})

	t.Run("hands a project-level domain to the app the deploy serves it on, and no other", func(t *testing.T) {
		t.Parallel()
		cfg := &project.Project{
			Domains: project.Domains{Production: []string{"acme.com"}},
			Apps:    []project.App{{Name: "web"}, {Name: "api"}},
		}

		served := edge.AttributeHostnames(cfg.Domains.Production, [][]string{nil, nil})
		urls := appurl.FormatProductionURLs(cfg)
		if got, want := urls["web"], "https://"+served[0][0]; got != want {
			t.Errorf("web url = %q, want %q: the deploy serves a project hostname on the first app `apps` names", got, want)
		}
		if got, ok := urls["api"]; ok {
			t.Errorf("api url = %q, want none: nothing serves api, so an url would name a host that answers for web", got)
		}
	})
}

func TestPreviewServesEachAppOnTheAliasTheProviderAnswered(t *testing.T) {
	t.Parallel()

	urls := appurl.FormatPreviewURLs(map[string]string{
		"web": "pr-1-web-abcdefghijklmnop12345678.preview.acme.com",
		"api": "",
	})
	if got, want := urls["web"], "https://pr-1-web-abcdefghijklmnop12345678.preview.acme.com"; got != want {
		t.Errorf("web url = %q, want %q", got, want)
	}
	if got, ok := urls["api"]; ok {
		t.Errorf("api url = %q, want none: nothing answered a hostname for it", got)
	}
}

func TestPrepend(t *testing.T) {
	t.Parallel()

	cfg := &project.Project{Apps: []project.App{
		{Name: "web", Serverless: &project.Serverless{Framework: buildoutput.FrameworkNext}},
		{Name: "api", Serverless: &project.Serverless{Framework: buildoutput.FrameworkGo}},
		{Name: "kit", Serverless: &project.Serverless{Framework: buildoutput.FrameworkSvelteKit}},
		{Name: "docs"},
	}}
	byApp := map[string][]variables.Variable{
		"web":  {{Key: "LOG_LEVEL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "info"}},
		"api":  nil,
		"kit":  nil,
		"docs": nil,
	}
	appurl.Prepend(cfg, byApp, map[string]string{"web": "https://acme.com", "api": "https://api.acme.com", "kit": "https://kit.acme.com"})

	variables := map[string]variables.Variable{}
	for _, v := range byApp["web"] {
		variables[v.Key] = v
	}
	if got, want := variables[processenv.AppURLEnvVar].Value, "https://acme.com"; got != want {
		t.Errorf("%s = %q, want %q", processenv.AppURLEnvVar, got, want)
	}
	if got, want := variables[processenv.NextPublicURLEnvVar].Value, "https://acme.com"; got != want {
		t.Errorf("%s = %q, want the same value mirrored for the browser bundle", processenv.NextPublicURLEnvVar, got)
	}
	if variables["LOG_LEVEL"].Value != "info" {
		t.Errorf("web variables = %+v, want the declared ones kept", byApp["web"])
	}
	if got := keys(byApp["api"]); !slices.Equal(got, []string{processenv.AppURLEnvVar}) {
		t.Errorf("api variables = %v, want only %s: nothing in a go app reads %s, and a value of its own under that name would be overwritten", got, processenv.AppURLEnvVar, processenv.NextPublicURLEnvVar)
	}
	if got := keys(byApp["kit"]); !slices.Equal(got, []string{processenv.AppURLEnvVar, processenv.SvelteKitPublicURLEnvVar}) {
		t.Errorf("kit variables = %v, want the deployment url under its own name and the one sveltekit hands the browser, and no other framework's", got)
	}
	if got := keys(byApp["web"]); slices.Contains(got, processenv.SvelteKitPublicURLEnvVar) {
		t.Errorf("web variables = %v, want no %s: only a sveltekit app reads it", got, processenv.SvelteKitPublicURLEnvVar)
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
