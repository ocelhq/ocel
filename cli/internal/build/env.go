package build

import (
	"context"
	"maps"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/localrpc"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

type AppVariables struct {
	Env             map[string]string
	Live            map[string]string
	BindingProxyEnv map[string]string
	PublicKeys      []string
}

func (v AppVariables) SecretValues() []string {
	secrets := SecretValues(v.Live)
	if token := v.BindingProxyEnv[localrpc.SessionTokenEnvVar]; token != "" {
		secrets = append(secrets, token)
	}
	return secrets
}

func SplitVariablesByClass(apps []variables.ClientApp, secrets map[string]map[string]string) map[string]AppVariables {
	byApp := make(map[string]AppVariables, len(apps))
	for _, app := range apps {
		values := AppVariables{Env: map[string]string{}, Live: map[string]string{}, PublicKeys: variables.PublicKeys(app.App, app.Declared)}
		for _, v := range app.Variables {
			switch v.Class {
			case resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN:
				values.Env[v.Key] = v.Value
			case resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE:
				values.Live[v.Key] = v.Value
			}
		}
		maps.Copy(values.Live, secrets[app.Name])
		byApp[app.Name] = values
	}
	return byApp
}

func RevealSecrets(ctx context.Context, declarations *variables.Declarations, cfg *project.Project, preBuild *project.LifecycleCommand) (map[string]map[string]string, error) {
	var apps []string
	for _, app := range cfg.Apps {
		if CanReadVariablesAtBuild(app) || app.RunsOn(provider.ComputeContainer) || preBuild != nil && preBuild.App == app.Name {
			apps = append(apps, app.Name)
		}
	}
	if len(apps) == 0 {
		return nil, nil
	}
	return declarations.RevealSecrets(ctx, apps)
}
