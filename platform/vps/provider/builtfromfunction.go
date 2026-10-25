package vps

import (
	"maps"
	"path"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/provider"
)

func newFunctionEnv(app *provider.AppSpec) map[string]string {
	env := maps.Clone(app.Values.ContainerEnv)
	if env == nil {
		env = map[string]string{}
	}
	if app.Router != "" {
		env[processenv.RouterKindEnvVar] = string(app.Router)
	}
	if app.Routing == nil {
		return env
	}
	maps.Copy(env, provider.NewOriginDispatchEnv())
	maps.Copy(env, provider.NewRouteTableEnv(app, path.Join(images.FunctionImageRoot, edge.NextRouteTableFile)))
	env[processenv.StorageCoordinateEnvVar] = app.StorageCoordinate.StorageCoordinate()
	return env
}
