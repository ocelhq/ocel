package provider

import (
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/processenv"
)

func NewOriginDispatchEnv() map[string]string {
	return map[string]string{edge.OriginDispatchVar: "1", edge.OriginSignedVar: "1"}
}

func NewRouteTableEnv(app *AppSpec, routeTable string) map[string]string {
	env := map[string]string{}
	if app.Routing == nil {
		return env
	}
	env[processenv.RouteTableEnvVar] = routeTable
	env[processenv.BuildIDEnvVar] = app.BuildID
	if rules := app.Static.Variable(); rules != "" {
		env[edge.StaticRulesVar] = rules
	}
	return env
}
