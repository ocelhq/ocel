package pulumi

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

const passphraseEnvVar = "PULUMI_CONFIG_PASSPHRASE"

type Access struct {
	BackendURL string

	Passphrase string

	Project string

	Env map[string]string
}

type Program interface {
	Run(ctx *pulumi.Context, plan providerkit.StackPlan) error
}

type Configurer interface {
	Configure(ctx context.Context, plan providerkit.StackPlan) (auto.ConfigMap, error)
}

type Config struct {
	Access Access

	Program Program
}

type Adapter struct {
	config Config
}

func New(config Config) *Adapter { return &Adapter{config: config} }

func (a *Adapter) Access() Access { return a.config.Access }

func (a *Adapter) StackName(ref providerkit.StackRef) string { return ref.Name.String() }

func (a *Adapter) ProjectName(ref providerkit.StackRef) string {
	if a.config.Access.Project != "" {
		return a.config.Access.Project
	}
	return naming.PulumiProject(naming.Sanitize(ref.Project))
}

type Setup struct {
	Project workspace.Project

	EnvVars map[string]string

	Options []auto.LocalWorkspaceOption
}

func (a *Adapter) Workspace(plan providerkit.StackPlan) (Setup, error) {
	access := a.config.Access
	switch {
	case access.BackendURL == "":
		return Setup{}, providerkit.Refuse(providerkit.CodeNotReady,
			"this provider names no state backend, and an engine run has nowhere to keep %s's state", plan.Ref.Name)
	case access.Passphrase == "":
		return Setup{}, providerkit.Refuse(providerkit.CodeNotReady,
			"this provider names no state passphrase, and %s's state would be written unsealed", plan.Ref.Name)
	case a.config.Program == nil:
		return Setup{}, providerkit.Refuse(providerkit.CodeNotReady,
			"this adapter carries no program, so there is nothing for the engine to run over %s", plan.Ref.Name)
	}

	project := workspace.Project{
		Name:    tokens.PackageName(a.ProjectName(plan.Ref)),
		Runtime: workspace.NewProjectRuntimeInfo("go", nil),
		Backend: &workspace.ProjectBackend{URL: access.BackendURL},
	}
	env := a.env()
	program := func(ctx *pulumi.Context) error { return a.config.Program.Run(ctx, plan) }

	return Setup{
		Project: project,
		EnvVars: env,
		Options: []auto.LocalWorkspaceOption{
			auto.Project(project),
			auto.EnvVars(env),
			auto.Program(program),
			auto.SecretsProvider("passphrase"),
		},
	}, nil
}

func (a *Adapter) env() map[string]string {
	env := map[string]string{passphraseEnvVar: a.config.Access.Passphrase}
	for _, key := range slices.Sorted(maps.Keys(a.config.Access.Env)) {
		env[key] = a.config.Access.Env[key]
	}
	return env
}

func (a *Adapter) Stack(ctx context.Context, plan providerkit.StackPlan) (auto.ConfigMap, error) {
	if _, err := a.Workspace(plan); err != nil {
		return nil, err
	}
	configurer, configures := a.config.Program.(Configurer)
	if !configures {
		return auto.ConfigMap{}, nil
	}
	return configurer.Configure(ctx, plan)
}

func (a *Adapter) Run(ctx context.Context, plan providerkit.StackPlan, _ providerkit.Reporter) (providerkit.StackResult, error) {
	if _, err := a.Stack(ctx, plan); err != nil {
		return providerkit.StackResult{}, err
	}
	return providerkit.StackResult{}, providerkit.Refuse(providerkit.CodeNotReady,
		"the pulumi adapter sets a workspace up but runs no engine yet, so %s is not stood up here", plan.Ref.Name)
}

func (a *Adapter) Destroy(ctx context.Context, ref providerkit.StackRef, _ providerkit.Reporter) error {
	if _, err := a.Workspace(providerkit.StackPlan{Ref: ref}); err != nil {
		return err
	}
	return providerkit.Refuse(providerkit.CodeNotReady,
		"the pulumi adapter sets a workspace up but runs no engine yet, so %s is not taken down here", ref.Name)
}

func Decode[T any](outputs auto.OutputMap) (T, error) {
	var into T
	plain := make(map[string]any, len(outputs))
	for name, output := range outputs {
		plain[name] = output.Value
	}
	raw, err := json.Marshal(plain)
	if err != nil {
		return into, fmt.Errorf("read the stack's outputs: %w", err)
	}
	if err := json.Unmarshal(raw, &into); err != nil {
		return into, fmt.Errorf("read the stack's outputs: %w", err)
	}
	return into, nil
}
