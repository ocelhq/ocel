package appurl

import (
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/processenv"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func FormatProductionURLs(cfg *project.Project) map[string]string {
	return byApp(cfg, cfg.Domains.Production, func(app project.App) []string {
		return app.ProductionDomains
	})
}

func FormatPreviewURLs(hostnames map[string]string) map[string]string {
	urls := make(map[string]string, len(hostnames))
	for app, hostname := range hostnames {
		if hostname != "" {
			urls[app] = "https://" + hostname
		}
	}
	return urls
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

func Variables(framework string, clientBundle bool, url string) []variables.Variable {
	if url == "" {
		return nil
	}
	var written []variables.Variable
	for _, v := range []variables.Variable{
		{Key: processenv.AppURLEnvVar, Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: url},
		{Key: processenv.ClientURLEnvVar, Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: url},
		{Key: processenv.SvelteKitPublicURLEnvVar, Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: url},
	} {
		if processenv.IsInjected(framework, clientBundle, v.Key) {
			written = append(written, v)
		}
	}
	return written
}

func Prepend(cfg *project.Project, byApp map[string][]variables.Variable, byURL map[string]string) {
	scoped := make(map[string]variables.App, len(cfg.Apps))
	for _, a := range variablescope.Apps(cfg) {
		scoped[a.Name] = a
	}
	for app, variables := range byApp {
		a := scoped[app]
		byApp[app] = append(Variables(a.Framework, a.ClientBundle, byURL[app]), variables...)
	}
}

func first(hosts []string) string {
	if len(hosts) == 0 {
		return ""
	}
	return hosts[0]
}
