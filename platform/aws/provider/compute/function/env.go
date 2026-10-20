package function

import (
	"maps"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/bucket"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/variables"
)

func AppEnv(spec provider.StackSpec, bundle variables.Bundle, sessions bucket.Sessions, stateTable string) map[string]string {
	app := spec.App
	env := map[string]string{}
	if app.Router != "" {
		env[processenv.RouterKindEnvVar] = string(app.Router)
	}
	if spec.Edge != nil {
		facts := spec.Edge.Facts()
		if !facts.RunsCode {
			env[edge.OriginDispatchVar] = "1"
			env[edge.OriginSignedVar] = "1"
		}
		if !isForwardedPastEdge(spec) {
			maps.Copy(env, edge.NewCachingOriginEnv(facts, spec.Ref.Name.Release.String()))
		}
	}
	if app.OcelRouteTable != nil {
		env[edge.OcelRouteTableVar] = ocelRouteTableInTask
	}
	if app.Proxied {
		env[bucket.EnvStateTable] = stateTable
		env[bucket.EnvSessionPrefix] = sessions.KeyPrefix
	}
	for key, value := range app.Values.Plain {
		env[key] = value
	}
	maps.Copy(env, app.Values.PhaseEnv())
	if app.Values.Folder != "" {
		env[processenv.AppFolderEnvVar] = app.Values.Folder
	}
	for key, value := range bundle.Env() {
		env[key] = value
	}
	return env
}

func isForwardedPastEdge(spec provider.StackSpec) bool {
	pairings := provider.Facts{Pairings: edges.Pairings()}
	return pairings.IsForwarded(spec.Edge.Kind(), spec.App.Router)
}
