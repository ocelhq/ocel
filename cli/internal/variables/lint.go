package variables

import (
	"fmt"
	"slices"
	"strings"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func LintFolders(definitions []*resourcesv1.VariableDefinition, apps []App, configPath string) ([]string, error) {
	bound := map[string]string{}
	for _, app := range apps {
		if app.Folder != "" {
			bound[app.Folder] = app.Name
		}
	}

	var warnings []string
	for _, definition := range definitions {
		scope := definition.GetFolders()
		if len(scope) == 0 {
			continue
		}

		var unbound []string
		for _, folder := range scope {
			if _, ok := bound[folder]; !ok {
				unbound = append(unbound, folder)
			}
		}

		switch {
		case len(unbound) == 0:
		case len(unbound) == len(scope):
			warnings = append(warnings, fmt.Sprintf(
				"%s is scoped to %s, which no app binds. Nothing reads it — bind an app to one of those folders in %s, or drop the scope.",
				definition.GetKey(), strings.Join(scope, " and "), configPath))
		default:
			return nil, fmt.Errorf(
				"%s is scoped to %s, but no app binds %s.\n"+
					"A scoped variable must diverge across every folder it names, so this is a folder rename that landed in one place only.\n"+
					"  declared in %s\n"+
					"  bound in    %s\n"+
					"Rename the folder in both, or drop %s from the scope",
				definition.GetKey(), strings.Join(scope, " and "), strings.Join(unbound, " or "),
				source(definition), configPath, strings.Join(unbound, " or "))
		}
	}
	return warnings, nil
}

func source(definition *resourcesv1.VariableDefinition) string {
	if s := definition.GetSource(); s != "" {
		return s
	}
	return "the defineEnv call declaring " + definition.GetKey()
}

func LintEdgeSecrets(definitions []*resourcesv1.VariableDefinition, apps []App, edgeApps []string) ([]string, error) {
	folder := make(map[string]string, len(apps))
	for _, app := range apps {
		folder[app.Name] = app.Folder
	}
	for _, app := range edgeApps {
		if _, known := folder[app]; !known {
			return nil, fmt.Errorf("%s ships edge entries but is not one of this project's apps (%s), so there is no folder to read its variables from",
				app, strings.Join(names(apps), " and "))
		}
	}

	var warnings []string
	for _, definition := range definitions {
		if definition.GetClass() != resourcesv1.VariableClass_VARIABLE_CLASS_SECRET {
			continue
		}

		var reached []string
		for _, app := range edgeApps {
			if scope := definition.GetFolders(); len(scope) > 0 && !slices.Contains(scope, folder[app]) {
				continue
			}
			reached = append(reached, app)
		}
		if len(reached) == 0 {
			continue
		}

		ships := "ships"
		if len(reached) > 1 {
			ships = "ship"
		}
		key := definition.GetKey()
		warnings = append(warnings, fmt.Sprintf(
			"%s is class secret, and %s %s edge entries. A secret is read live on every request and the edge has no live channel to read one over, so an edge entry reading %s throws. Move those entries to the nodejs runtime, or declare %s as sensitive in %s.",
			key, strings.Join(reached, " and "), ships, key, key, source(definition)))
	}
	return warnings, nil
}

func names(apps []App) []string {
	list := make([]string, 0, len(apps))
	for _, app := range apps {
		list = append(list, app.Name)
	}
	return list
}
