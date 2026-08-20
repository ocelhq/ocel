package deploy

import (
	"fmt"
	"maps"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type originGuard struct {
	Entry  string
	Secret string
}

func resolveOriginGuard(cfg Config, app *deploymentsv1.ManifestApp) (*originGuard, error) {
	if cfg.Edge == nil {
		return nil, nil
	}
	if _, programmable := cfg.Edge.(edge.Programmable); programmable {
		return nil, nil
	}
	if edge.SignsOriginForwards(cfg.Edge) {
		return nil, nil
	}
	name := app.GetName()
	desc, ok, err := readServeDescriptor(cfg.ArtifactRoot, name)
	if err != nil {
		return nil, err
	}
	if !ok || desc.Entry == "" {
		return nil, nil
	}
	if cfg.OriginSecret == "" {
		return nil, fmt.Errorf(
			"the %s edge reaches %s over a Function URL no signature guards, and this substrate holds no secret for the entry function to demand of it; re-run `ocel bootstrap`",
			cfg.Edge.Kind(), name,
		)
	}
	return &originGuard{Entry: desc.Entry, Secret: cfg.OriginSecret}, nil
}

func (f *originGuard) hosts(fn *deploymentsv1.ManifestFunction) bool {
	return f != nil && routeID(fn) == f.Entry
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
