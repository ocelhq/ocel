package build

import (
	"github.com/ocelhq/ocel/cli/internal/clientenv"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

type AppVariables struct {
	Env  map[string]string
	Live map[string]string
}

func VariablesOf(apps []clientenv.App) map[string]AppVariables {
	byApp := make(map[string]AppVariables, len(apps))
	for _, app := range apps {
		values := AppVariables{Env: map[string]string{}, Live: map[string]string{}}
		for _, v := range app.Variables {
			switch v.Class {
			case resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN:
				values.Env[v.Key] = v.Value
			case resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE:
				values.Live[v.Key] = v.Value
			}
		}
		byApp[app.Name] = values
	}
	return byApp
}
