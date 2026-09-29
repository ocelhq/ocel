package appurl

import (
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	"github.com/ocelhq/ocel/pkg/appbuild"
	"github.com/ocelhq/ocel/pkg/constants"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
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
		apps = []projectconfig.App{{Name: variablescope.RootApp}}
	}
	own := make([][]string, len(apps))
	for slot, app := range apps {
		own[slot] = declared(app)
	}

	urls := make(map[string]string, len(apps))
	for slot, served := range appbuild.AttributeHostnames(project, own) {
		if host := first(served); host != "" {
			urls[apps[slot].Name] = "https://" + host
		}
	}
	return urls
}

func Variables(clientBundle bool, url string) []manifestbuilder.Variable {
	if url == "" {
		return nil
	}
	var written []manifestbuilder.Variable
	for _, v := range []manifestbuilder.Variable{
		{Key: constants.AppURLEnvName, Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: url},
		{Key: appbuild.ClientURLEnvName, Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: url, ClientAccessible: true},
	} {
		if appbuild.IsOcelInjectedEnv(clientBundle, v.Key) {
			written = append(written, v)
		}
	}
	return written
}

func clientBundles(cfg *projectconfig.Config) map[string]bool {
	apps := variablescope.Apps(cfg)
	byName := make(map[string]bool, len(apps))
	for _, a := range apps {
		byName[a.Name] = a.ClientBundle
	}
	return byName
}

func Prepend(cfg *projectconfig.Config, byApp map[string][]manifestbuilder.Variable, byURL map[string]string) {
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
