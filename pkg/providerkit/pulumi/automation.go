package pulumi

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/events"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optdestroy"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optpreview"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optup"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/config"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	passphraseEnvVar = "PULUMI_CONFIG_PASSPHRASE"
	backendEnvVar    = "PULUMI_BACKEND_URL"
)

const DefaultParallel = 64

type Operation string

const (
	OperationProvision Operation = "provision"
	OperationDestroy   Operation = "destroy"
)

type Backend struct {
	URL string

	Passphrase string

	Project string

	Env map[string]string
}

type Engine interface {
	Preview(ctx context.Context, setup WorkspaceSpec, op Operation, progress edge.Progress) ([]provider.Change, error)

	Up(ctx context.Context, setup WorkspaceSpec, progress edge.Progress) (auto.OutputMap, error)

	Destroy(ctx context.Context, setup WorkspaceSpec, progress edge.Progress) error

	Outputs(ctx context.Context, setup WorkspaceSpec) (auto.OutputMap, error)
}

type Config struct {
	Backend Backend

	Program func(ctx *pulumi.Context, spec provider.StackSpec) error

	Configure func(ctx context.Context, spec provider.StackSpec) (auto.ConfigMap, error)

	Decode func(ctx context.Context, spec provider.StackSpec, outputs auto.OutputMap) (provider.StackResult, error)

	Parallel int

	Refresh func(ref provider.StackRef, op Operation) bool

	Engine Engine

	Plugins []Plugin
}

type Automation struct {
	config  Config
	plugins *hostedPlugins
}

func New(config Config) *Automation {
	return &Automation{config: config, plugins: &hostedPlugins{declared: config.Plugins}}
}

func (a *Automation) Backend() Backend { return a.config.Backend }

func (a *Automation) StackName(ref provider.StackRef) string { return ref.Name.String() }

func (a *Automation) ProjectName(ref provider.StackRef) string {
	if a.config.Backend.Project != "" {
		return a.config.Backend.Project
	}
	return naming.PulumiProject(naming.Sanitize(ref.Project))
}

type WorkspaceSpec struct {
	Ref provider.StackRef

	Stack string

	Project workspace.Project

	Program pulumi.RunFunc

	EnvVars map[string]string

	Options []auto.LocalWorkspaceOption

	Config auto.ConfigMap

	Parallel int

	Refresh bool
}

func (a *Automation) Workspace(spec provider.StackSpec) (WorkspaceSpec, error) {
	return a.workspace(spec, OperationProvision)
}

func (a *Automation) workspace(spec provider.StackSpec, op Operation) (WorkspaceSpec, error) {
	backend := a.config.Backend
	switch {
	case backend.URL == "":
		return WorkspaceSpec{}, refusal.Refuse(refusal.CodeNotReady,
			"this provider names no state backend, and an engine run has nowhere to keep %s's state", spec.Ref.Name)
	case backend.Passphrase == "":
		return WorkspaceSpec{}, refusal.Refuse(refusal.CodeNotReady,
			"this provider names no state passphrase, and %s's state would be written unsealed", spec.Ref.Name)
	case a.config.Program == nil:
		return WorkspaceSpec{}, refusal.Refuse(refusal.CodeNotReady,
			"this automation carries no program, so there is nothing for the engine to run over %s", spec.Ref.Name)
	}

	project := workspace.Project{
		Name:    tokens.PackageName(a.ProjectName(spec.Ref)),
		Runtime: workspace.NewProjectRuntimeInfo("go", nil),
		Backend: &workspace.ProjectBackend{URL: backend.URL},
	}
	env, err := a.env()
	if err != nil {
		return WorkspaceSpec{}, err
	}
	program := func(ctx *pulumi.Context) error { return a.config.Program(ctx, spec) }

	return WorkspaceSpec{
		Ref:      spec.Ref,
		Stack:    a.StackName(spec.Ref),
		Project:  project,
		Program:  program,
		EnvVars:  env,
		Parallel: a.parallel(),
		Refresh:  a.refreshes(spec.Ref, op),
		Options: []auto.LocalWorkspaceOption{
			auto.Project(project),
			auto.EnvVars(env),
			auto.Program(program),
			auto.SecretsProvider("passphrase"),
		},
	}, nil
}

func (a *Automation) parallel() int {
	if a.config.Parallel > 0 {
		return a.config.Parallel
	}
	return DefaultParallel
}

func (a *Automation) refreshes(ref provider.StackRef, op Operation) bool {
	return a.config.Refresh != nil && a.config.Refresh(ref, op)
}

func (a *Automation) env() (map[string]string, error) {
	env := map[string]string{
		passphraseEnvVar:           a.config.Backend.Passphrase,
		backendEnvVar:              a.config.Backend.URL,
		"PULUMI_SKIP_CHECKPOINTS":  "true",
		"PULUMI_SKIP_UPDATE_CHECK": "true",
	}
	for _, key := range slices.Sorted(maps.Keys(a.config.Backend.Env)) {
		env[key] = a.config.Backend.Env[key]
	}
	attached, err := a.plugins.attach()
	if err != nil {
		return nil, err
	}
	if attached != "" {
		env[debugProvidersEnvVar] = attached
	}
	return env, nil
}

func (a *Automation) StackConfig(ctx context.Context, spec provider.StackSpec) (auto.ConfigMap, error) {
	if _, err := a.Workspace(spec); err != nil {
		return nil, err
	}
	if a.config.Configure == nil {
		return auto.ConfigMap{}, nil
	}
	return a.config.Configure(ctx, spec)
}

func (a *Automation) setup(ctx context.Context, spec provider.StackSpec, op Operation, progress edge.Progress) (WorkspaceSpec, error) {
	setup, err := a.workspace(spec, op)
	if err != nil {
		return WorkspaceSpec{}, err
	}
	if setup.Config, err = a.StackConfig(ctx, spec); err != nil {
		return WorkspaceSpec{}, err
	}
	if a.config.Engine == nil {
		command, err := pinned.install(ctx, progress)
		if err != nil {
			return WorkspaceSpec{}, err
		}
		setup.Options = append(setup.Options, auto.Pulumi(command))
	}
	return setup, nil
}

func (a *Automation) engine() Engine {
	if a.config.Engine != nil {
		return a.config.Engine
	}
	return autoEngine{}
}

func (a *Automation) Preview(ctx context.Context, spec provider.StackSpec, progress edge.Progress) (provider.Plan, error) {
	return a.preview(ctx, spec, OperationProvision, progress)
}

func (a *Automation) PreviewDestroy(ctx context.Context, ref provider.StackRef, progress edge.Progress) (provider.Plan, error) {
	return a.preview(ctx, provider.StackSpec{Ref: ref}, OperationDestroy, progress)
}

func (a *Automation) preview(ctx context.Context, spec provider.StackSpec, op Operation, progress edge.Progress) (provider.Plan, error) {
	setup, err := a.setup(ctx, spec, op, progress)
	if err != nil {
		return provider.Plan{}, err
	}
	changes, err := a.engine().Preview(ctx, setup, op, progress)
	if err != nil {
		return provider.Plan{}, busy(err, setup)
	}
	if len(changes) == 0 {
		return provider.Plan{}, nil
	}
	group := provider.ChangeGroup{
		Kind:    provider.StackGroupKind,
		Name:    setup.Stack,
		Changes: changes,
	}
	group.Action, group.Reason = provider.RollUp(changes)
	return provider.Plan{Groups: []provider.ChangeGroup{group}}, nil
}

const stackResourceType = "pulumi:pulumi:Stack"

func planRows(mutations, standing []apitype.StepEventMetadata) []provider.Change {
	rows := make(map[string]provider.Change, len(mutations)+len(standing))
	for _, step := range mutations {
		action, mutates := plannedAction(step.Op)
		if !mutates || step.Type == stackResourceType {
			continue
		}
		rows[step.URN] = row(step, action)
	}
	for _, step := range standing {
		if step.Op != apitype.OpSame || step.Type == stackResourceType {
			continue
		}
		if _, mutating := rows[step.URN]; mutating {
			continue
		}
		rows[step.URN] = row(step, provider.ActionKeep)
	}
	changes := make([]provider.Change, 0, len(rows))
	for _, urn := range slices.Sorted(maps.Keys(rows)) {
		changes = append(changes, rows[urn])
	}
	return changes
}

func row(step apitype.StepEventMetadata, action provider.ChangeAction) provider.Change {
	return provider.Change{
		Kind:   capIdentifier(step.Type),
		Name:   resourceNameFromURN(step.URN),
		Action: action,
	}
}

func plannedAction(op apitype.OpType) (provider.ChangeAction, bool) {
	switch op {
	case apitype.OpCreate, apitype.OpCreateReplacement, apitype.OpImport:
		return provider.ActionCreate, true
	case apitype.OpUpdate:
		return provider.ActionUpdate, true
	case apitype.OpReplace, apitype.OpImportReplacement:
		return provider.ActionReplace, true
	case apitype.OpDelete, apitype.OpDeleteReplaced:
		return provider.ActionDelete, true
	default:
		return "", false
	}
}

func (a *Automation) Run(ctx context.Context, spec provider.StackSpec, progress edge.Progress) (provider.StackResult, error) {
	setup, err := a.setup(ctx, spec, OperationProvision, progress)
	if err != nil {
		return provider.StackResult{}, err
	}
	outputs, err := a.engine().Up(ctx, setup, progress)
	if err != nil {
		return provider.StackResult{}, busy(err, setup)
	}
	if a.config.Decode == nil {
		return provider.StackResult{}, nil
	}
	return a.config.Decode(ctx, spec, outputs)
}

func (a *Automation) Destroy(ctx context.Context, ref provider.StackRef, progress edge.Progress) error {
	setup, err := a.setup(ctx, provider.StackSpec{Ref: ref}, OperationDestroy, progress)
	if err != nil {
		return err
	}
	if err := a.engine().Destroy(ctx, setup, progress); err != nil {
		return busy(err, setup)
	}
	return nil
}

func (a *Automation) Outputs(ctx context.Context, ref provider.StackRef, progress edge.Progress) (auto.OutputMap, error) {
	setup, err := a.setup(ctx, provider.StackSpec{Ref: ref}, OperationProvision, progress)
	if err != nil {
		return nil, err
	}
	return a.engine().Outputs(ctx, setup)
}

const lockedMessage = "the stack is currently locked"

func busy(err error, setup WorkspaceSpec) error {
	if err == nil || !strings.Contains(err.Error(), lockedMessage) {
		return err
	}
	return refusal.Refuse(refusal.CodeBusy,
		"%s is locked by a run that is either still working or was killed."+
			"\n\nconfirm no deploy or teardown is running against this stack, then release it with:"+
			"\n  PULUMI_BACKEND_URL=%s PULUMI_CONFIG_PASSPHRASE=<the account passphrase> pulumi cancel --stack %s"+
			"\nand run this again",
		setup.Stack, setup.Project.Backend.URL, setup.Stack)
}

type autoEngine struct{}

func (autoEngine) Preview(ctx context.Context, setup WorkspaceSpec, op Operation, progress edge.Progress) ([]provider.Change, error) {
	stack, err := auto.UpsertStackInlineSource(ctx, setup.Stack, string(setup.Project.Name), setup.Program, setup.Options...)
	if err != nil {
		return nil, fmt.Errorf("prepare stack %s: %w", setup.Stack, err)
	}
	if err := applyConfig(ctx, stack, setup.Config); err != nil {
		return nil, err
	}

	engineEvents := make(chan events.EngineEvent, 256)
	rows := drainRows(engineEvents)
	if progress != nil {
		progress.Say("Working out what would change")
	}

	if op == OperationDestroy {
		_, err = stack.PreviewDestroy(ctx, optdestroy.EventStreams(engineEvents), optdestroy.Parallel(setup.Parallel))
	} else {
		_, err = stack.Preview(ctx, optpreview.EventStreams(engineEvents), optpreview.Parallel(setup.Parallel))
	}
	if err != nil {
		return nil, fmt.Errorf("plan stack %s: %w", setup.Stack, err)
	}
	drained, err := awaitRows(rows, engineDrainGrace)
	if err != nil {
		return nil, fmt.Errorf("plan stack %s: %w", setup.Stack, err)
	}
	return drained, nil
}

func drainRows(engineEvents <-chan events.EngineEvent) <-chan []provider.Change {
	drained := make(chan []provider.Change, 1)
	go func() {
		var mutations, standing []apitype.StepEventMetadata
		for ev := range engineEvents {
			switch {
			case ev.ResourcePreEvent != nil:
				mutations = append(mutations, ev.ResourcePreEvent.Metadata)
			case ev.ResOutputsEvent != nil:
				standing = append(standing, ev.ResOutputsEvent.Metadata)
			}
		}
		drained <- planRows(mutations, standing)
	}()
	return drained
}

func awaitRows(drained <-chan []provider.Change, grace time.Duration) ([]provider.Change, error) {
	select {
	case rows := <-drained:
		return rows, nil
	case <-time.After(grace):
		return nil, fmt.Errorf(
			"the engine's plan rows did not drain within %s, and the rows that did arrive would read as a plan doing less than the run would do", grace)
	}
}

func (autoEngine) Up(ctx context.Context, setup WorkspaceSpec, progress edge.Progress) (auto.OutputMap, error) {
	stack, err := auto.UpsertStackInlineSource(ctx, setup.Stack, string(setup.Project.Name), setup.Program, setup.Options...)
	if err != nil {
		return nil, fmt.Errorf("prepare stack %s: %w", setup.Stack, err)
	}
	if err := applyConfig(ctx, stack, setup.Config); err != nil {
		return nil, err
	}

	lines := detailWriter(progress)
	opts := []optup.Option{optup.Parallel(setup.Parallel)}
	if lines != nil {
		opts = append(opts, optup.ProgressStreams(lines))
	}
	if setup.Refresh {
		opts = append(opts, optup.Refresh())
	}

	engineEvents := make(chan events.EngineEvent, 256)
	traced := drainTrace(engineEvents, resourceLatencyOutlierThreshold)
	opts = append(opts, optup.EventStreams(engineEvents))

	start := time.Now()
	res, upErr := stack.Up(ctx, opts...)
	end := time.Now()
	lines.Flush()

	trace := awaitTrace(traced, engineDrainGrace)
	if trace.Start.IsZero() {
		trace.Start, trace.End = start, end
	}
	reportTrace(progress, trace, upErr)

	if upErr != nil {
		return nil, fmt.Errorf("provision stack %s: %w", setup.Stack, upErr)
	}
	return res.Outputs, nil
}

func (autoEngine) Destroy(ctx context.Context, setup WorkspaceSpec, progress edge.Progress) error {
	stack, err := auto.SelectStackInlineSource(ctx, setup.Stack, string(setup.Project.Name), nil, setup.Options...)
	if auto.IsSelectStack404Error(err) {
		if progress != nil {
			progress.Say("No stack " + setup.Stack + " to destroy")
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("select stack %s: %w", setup.Stack, err)
	}

	if progress != nil {
		progress.Say("Destroying resources (this can take several minutes)")
	}
	lines := detailWriter(progress)
	opts := []optdestroy.Option{optdestroy.Parallel(setup.Parallel)}
	if lines != nil {
		opts = append(opts, optdestroy.ProgressStreams(lines))
	}
	if setup.Refresh {
		opts = append(opts, optdestroy.Refresh())
	}
	if _, err := stack.Destroy(ctx, opts...); err != nil {
		lines.Flush()
		return fmt.Errorf("destroy stack %s: %w", setup.Stack, err)
	}
	lines.Flush()

	if err := stack.Workspace().RemoveStack(ctx, setup.Stack); err != nil {
		return fmt.Errorf("remove stack %s: %w", setup.Stack, err)
	}
	return nil
}

func (autoEngine) Outputs(ctx context.Context, setup WorkspaceSpec) (auto.OutputMap, error) {
	stack, err := auto.SelectStackInlineSource(ctx, setup.Stack, string(setup.Project.Name), nil, setup.Options...)
	if auto.IsSelectStack404Error(err) {
		return auto.OutputMap{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select stack %s: %w", setup.Stack, err)
	}
	outputs, err := stack.Outputs(ctx)
	if err != nil {
		return nil, fmt.Errorf("read what stack %s already provisions: %w", setup.Stack, err)
	}
	return outputs, nil
}

func applyConfig(ctx context.Context, stack auto.Stack, values auto.ConfigMap) error {
	if len(values) == 0 {
		return nil
	}
	ws := stack.Workspace()
	settings, err := ws.StackSettings(ctx, stack.Name())
	if err != nil {
		settings = &workspace.ProjectStack{}
	}
	if settings.Config == nil {
		settings.Config = config.Map{}
	}
	for _, name := range slices.Sorted(maps.Keys(values)) {
		key, err := config.ParseKey(name)
		if err != nil {
			return fmt.Errorf("read config key %s: %w", name, err)
		}
		settings.Config[key] = configValue(values[name])
	}
	if err := ws.SaveStackSettings(ctx, stack.Name(), settings); err != nil {
		return fmt.Errorf("configure %s: %w", stack.Name(), err)
	}
	return nil
}

func configValue(value auto.ConfigValue) config.Value {
	if value.Secret {
		return config.NewSecureValue(value.Value)
	}
	if structured(value.Value) {
		return config.NewObjectValue(value.Value)
	}
	return config.NewValue(value.Value)
}

func structured(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return false
	}
	return json.Valid([]byte(trimmed))
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
