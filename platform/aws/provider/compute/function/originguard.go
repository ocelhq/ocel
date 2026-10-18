package function

import (
	"maps"

	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/functionurl"
)

type OriginGuard struct {
	RootFunction string
	Secret       string
	Previous     string
}

func (f *OriginGuard) hosts(fn AppFunction) bool {
	return f != nil && fn.route() == f.RootFunction
}

func (f *OriginGuard) rootFunctionEnv(base map[string]string) map[string]string {
	if f == nil {
		return base
	}
	env := make(map[string]string, len(base)+3)
	maps.Copy(env, base)
	delete(env, edge.OriginSignedVar)
	component.GuardOrigin(env, f.Secret, f.Previous)
	return env
}

func (f *OriginGuard) rootFunctionURLAuth() string {
	if f == nil {
		return functionurl.AuthIAM
	}
	return functionurl.AuthNone
}

func NewOriginGuard(app *provider.AppSpec, tier environment.Tier, secret, previous string) (*OriginGuard, error) {
	guard := app.Guard
	if guard == nil {
		return nil, nil
	}
	if secret == "" {
		return nil, fmt.Errorf(
			"the edge reaches %s over a Function URL no signature guards, and this bootstrap has no secret for the root function to demand of it; re-run `%s`",
			app.App, provider.BootstrapCommand(tier))
	}
	return &OriginGuard{RootFunction: guard.RootFunction, Secret: secret, Previous: previous}, nil
}
