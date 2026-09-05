package appurl

import (
	"github.com/ocelhq/ocel/cli/internal/envwire"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

func Production(cfg *projectconfig.Config) map[string]string {
	return byApp(cfg, cfg.Domains["production"], func(app projectconfig.App) []string {
		return app.Domains["production"]
	})
}

func Preview(cfg *projectconfig.Config, host func(app string) string) map[string]string {
	return byApp(cfg, nil, func(app projectconfig.App) []string {
		if len(cfg.Apps) < 2 {
			return []string{host("")}
		}
		return []string{host(app.Name)}
	})
}

func byApp(cfg *projectconfig.Config, project []string, declared func(projectconfig.App) []string) map[string]string {
	apps := cfg.Apps
	if len(apps) == 0 {
		apps = []projectconfig.App{{Name: envwire.RootApp}}
	}
	own := make([][]string, len(apps))
	for slot, app := range apps {
		own[slot] = declared(app)
	}

	urls := make(map[string]string, len(apps))
	for slot, served := range providerkit.AttributeHostnames(project, own) {
		if host := first(served); host != "" {
			urls[apps[slot].Name] = "https://" + host
		}
	}
	return urls
}

func Variables(url string) []manifestbuilder.Variable {
	if url == "" {
		return nil
	}
	return []manifestbuilder.Variable{
		{Key: providerkit.URLEnvName, Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: url},
		{Key: providerkit.ClientURLEnvName, Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: url, ClientAccessible: true},
	}
}

func Prepend(byApp map[string][]manifestbuilder.Variable, byURL map[string]string) {
	for app, variables := range byApp {
		byApp[app] = append(Variables(byURL[app]), variables...)
	}
}

func BuildEnv(byURL map[string]string) map[string]map[string]string {
	byApp := make(map[string]map[string]string, len(byURL))
	for app, url := range byURL {
		env := make(map[string]string, 2)
		for _, v := range Variables(url) {
			env[v.Key] = v.Value
		}
		byApp[BuildKey(app)] = env
	}
	return byApp
}

func BuildKey(app string) string {
	if app == envwire.RootApp {
		return ""
	}
	return app
}

func first(hosts []string) string {
	if len(hosts) == 0 {
		return ""
	}
	return hosts[0]
}
