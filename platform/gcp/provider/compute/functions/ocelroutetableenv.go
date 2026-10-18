package functions

import (
	"path"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/provider"
)

var ocelRouteTableInImage = path.Join(images.FunctionImageRoot, edge.OcelRouteTableFile)

func newOcelRouteTableEnv(spec provider.StackSpec) map[string]string {
	if spec.App.OcelRouteTable == nil {
		return nil
	}
	return map[string]string{edge.OcelRouteTableVar: ocelRouteTableInImage}
}
