package gcp

import (
	"path"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/provider"
)

var ocelRoutesInImage = path.Join(images.FunctionImageRoot, edge.OcelRoutesFile)

func newOcelRoutesEnv(spec provider.StackSpec) map[string]string {
	if spec.App.OcelRoutes == nil {
		return nil
	}
	return map[string]string{edge.RoutesVar: ocelRoutesInImage}
}
