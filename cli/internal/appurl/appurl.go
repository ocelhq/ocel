package appurl

import (
	"cmp"

	"github.com/ocelhq/ocel/cli/internal/envwire"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	Name       = providerkit.URLEnvName
	ClientName = providerkit.ClientURLEnvName
)

func Production(cfg *projectconfig.Config) map[string]string {
	project := first(cfg.Domains["production"])
	if len(cfg.Apps) == 0 {
		return urls(map[string]string{envwire.RootApp: project})
	}
	hosts := make(map[string]string, len(cfg.Apps))
	for _, app := range cfg.Apps {
		hosts[app.Name] = cmp.Or(first(app.Domains["production"]), project)
	}
	return urls(hosts)
}

func Preview(cfg *projectconfig.Config, host func(app string) string) map[string]string {
	if len(cfg.Apps) == 0 {
		return urls(map[string]string{envwire.RootApp: host("")})
	}
	hosts := make(map[string]string, len(cfg.Apps))
	for _, app := range cfg.Apps {
		named := app.Name
		if len(cfg.Apps) < 2 {
			named = ""
		}
		hosts[app.Name] = host(named)
	}
	return urls(hosts)
}

func Variables(url string) []manifestbuilder.Variable {
	if url == "" {
		return nil
	}
	return []manifestbuilder.Variable{
		{Key: Name, Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: url},
		{Key: ClientName, Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: url, ClientAccessible: true},
	}
}

func Add(byApp map[string][]manifestbuilder.Variable, byURL map[string]string) {
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
		byApp[BuiltApp(app)] = env
	}
	return byApp
}

func BuiltApp(app string) string {
	if app == envwire.RootApp {
		return ""
	}
	return app
}

func urls(hosts map[string]string) map[string]string {
	out := make(map[string]string, len(hosts))
	for app, host := range hosts {
		if host == "" {
			continue
		}
		out[app] = "https://" + host
	}
	return out
}

func first(hosts []string) string {
	if len(hosts) == 0 {
		return ""
	}
	return hosts[0]
}
