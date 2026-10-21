package appurl

import (
	"net/url"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/processenv"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func FormatProductionURLs(cfg *project.Project) map[string]string {
	return byApp(cfg, cfg.Domains.Production, func(app project.App) []string {
		return app.ProductionDomains
	})
}

func byApp(cfg *project.Project, projectHosts []string, declared func(project.App) []string) map[string]string {
	apps := cfg.Apps
	own := make([][]string, len(apps))
	for slot, app := range apps {
		own[slot] = declared(app)
	}

	urls := make(map[string]string, len(apps))
	for slot, served := range edge.AttributeHostnames(projectHosts, own) {
		if host := first(served); host != "" {
			urls[apps[slot].Name] = "https://" + host
		}
	}
	return urls
}

func Variables(framework, appURL string) []variables.Variable {
	if appURL == "" {
		return nil
	}
	var written []variables.Variable
	for _, v := range []variables.Variable{
		{Key: processenv.AppURLEnvVar, Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: appURL},
		{Key: processenv.NextPublicURLEnvVar, Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: appURL},
		{Key: processenv.SvelteKitPublicURLEnvVar, Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: appURL},
		{Key: processenv.NextMetadataBaseHostEnvVar, Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: hostOf(appURL)},
	} {
		if processenv.IsInjected(framework, v.Key) {
			written = append(written, v)
		}
	}
	return written
}

func Prepend(cfg *project.Project, byApp map[string][]variables.Variable, byURL map[string]string) {
	scoped := make(map[string]variables.App, len(cfg.Apps))
	for _, a := range variables.AppsOf(cfg) {
		scoped[a.Name] = a
	}
	for app, variables := range byApp {
		a := scoped[app]
		byApp[app] = append(Variables(a.Framework, byURL[app]), variables...)
	}
}

func hostOf(appURL string) string {
	parsed, err := url.Parse(appURL)
	if err != nil {
		return ""
	}
	return parsed.Host
}

func first(hosts []string) string {
	if len(hosts) == 0 {
		return ""
	}
	return hosts[0]
}
