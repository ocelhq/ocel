package appurl

import (
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/processenv"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func Production(cfg *project.Project) map[string]string {
	return byApp(cfg, cfg.Domains.Production, func(app project.App) []string {
		return app.ProductionDomains
	})
}

func Preview(cfg *project.Project, host func(app string) string) map[string]string {
	return byApp(cfg, nil, func(app project.App) []string {
		if len(cfg.Apps) < 2 {
			return []string{host("")}
		}
		return []string{host(app.Name)}
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

func Variables(clientBundle bool, url string) []variables.Variable {
	if url == "" {
		return nil
	}
	var written []variables.Variable
	for _, v := range []variables.Variable{
		{Key: processenv.AppURLEnvVar, Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: url},
		{Key: processenv.ClientURLEnvVar, Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: url, ClientAccessible: true},
	} {
		if processenv.IsInjected(clientBundle, v.Key) {
			written = append(written, v)
		}
	}
	return written
}

func clientBundles(cfg *project.Project) map[string]bool {
	apps := variablescope.Apps(cfg)
	byName := make(map[string]bool, len(apps))
	for _, a := range apps {
		byName[a.Name] = a.ClientBundle
	}
	return byName
}

func Prepend(cfg *project.Project, byApp map[string][]variables.Variable, byURL map[string]string) {
	bundles := clientBundles(cfg)
	for app, variables := range byApp {
		byApp[app] = append(Variables(bundles[app], byURL[app]), variables...)
	}
}

func first(hosts []string) string {
	if len(hosts) == 0 {
		return ""
	}
	return hosts[0]
}
