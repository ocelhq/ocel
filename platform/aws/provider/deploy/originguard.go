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
	env := make(map[string]string, len(base)+1)
	maps.Copy(env, base)
	delete(env, edge.OriginSignedVar)
	env[edge.OriginSecretVar] = f.Secret
	if f.Previous != "" {
		env[edge.OriginSecretPreviousVar] = f.Previous
	}
	return env
}

func (f *originGuard) rootFunctionURLAuth() string {
	if f == nil {
		return functionurl.AuthIAM
	}
	return functionurl.AuthNone
}
