package build

import (
	"github.com/ocelhq/ocel/cli/internal/clientenv"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func Env(apps []clientenv.App) map[string]map[string]string {
	byApp := make(map[string]map[string]string, len(apps))
	for _, app := range apps {
		env := make(map[string]string)
		for _, v := range app.Variables {
			if v.Class == resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN {
				env[v.Key] = v.Value
			}
		}
		byApp[app.Name] = env
	}
	return byApp
}
