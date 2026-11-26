package deployreport

import (
	"github.com/ocelhq/ocel/cli/internal/variables"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

func ReleasedValues(resolved []variables.Variable) []*consolev1.ReleasedValue {
	var released []*consolev1.ReleasedValue
	for _, v := range resolved {
		if v.Version <= 0 || v.Class == resourcesv1.VariableClass_VARIABLE_CLASS_SECRET {
			continue
		}
		released = append(released, &consolev1.ReleasedValue{Key: v.Key, Folder: v.Folder, Environment: v.Environment, Version: v.Version})
	}
	return released
}
