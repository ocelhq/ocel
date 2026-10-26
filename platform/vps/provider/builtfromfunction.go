package vps

import (
	"maps"

	"github.com/ocelhq/ocel/pkg/edge"
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
	return env
}

func newFrontedEnv(spec provider.StackSpec, purgeSecret string) map[string]string {
	app := spec.App
	env := edge.NewCachingOriginEnv(spec.Edge.Facts(), spec.Ref.Name.Release.String())
	env[processenv.StorageCoordinateEnvVar] = app.StorageCoordinate.StorageCoordinate()
	env[edge.ImageEndpointVar] = "1"
	if app.ISR != nil && app.ISR.PurgeURL != "" && purgeSecret != "" {
		env[edge.PurgeURLVar] = app.ISR.PurgeURL
		env[edge.PurgeSecretVar] = purgeSecret
	}
	return env
}
