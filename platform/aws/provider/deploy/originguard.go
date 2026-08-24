package deploy

import (
	"maps"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type originGuard struct {
	Entry  string
	Secret string
}

func (f *originGuard) hosts(fn appFunction) bool {
	return f != nil && fn.route() == f.Entry
}

func (f *originGuard) entryEnv(base map[string]string) map[string]string {
	if f == nil {
		return base
	}
	env := make(map[string]string, len(base)+1)
	maps.Copy(env, base)
	delete(env, edge.OriginSignedVar)
	env[edge.OriginSecretVar] = f.Secret
	return env
}

func (f *originGuard) entryURLAuth() string {
	if f == nil {
		return functionURLAuthIAM
	}
	return functionURLAuthNone
}
