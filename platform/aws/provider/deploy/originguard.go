package deploy

import (
	"maps"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/platform/aws/provider/functionurl"
)

type originGuard struct {
	RootFunction string
	Secret       string
	Previous     string
}

func (f *originGuard) hosts(fn appFunction) bool {
	return f != nil && fn.route() == f.RootFunction
}

func (f *originGuard) rootFunctionEnv(base map[string]string) map[string]string {
	if f == nil {
		return base
	}
	env := make(map[string]string, len(base)+3)
	maps.Copy(env, base)
	delete(env, edge.OriginSignedVar)
	f.front(env)
	return env
}

func (f *originGuard) front(env map[string]string) {
	if f == nil {
		return
	}
	env[edge.OriginFrontedVar] = "1"
	env[edge.OriginSecretVar] = f.Secret
	if f.Previous != "" {
		env[edge.OriginSecretPreviousVar] = f.Previous
	}
}

func (f *originGuard) rootFunctionURLAuth() string {
	if f == nil {
		return functionurl.AuthIAM
	}
	return functionurl.AuthNone
}
