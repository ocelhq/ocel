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
	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	passphraseEnvVar = "PULUMI_CONFIG_PASSPHRASE"
	backendEnvVar    = "PULUMI_BACKEND_URL"
)

const DefaultParallel = 64

type Op string

const (
	OpProvision Op = "provision"
	OpDestroy   Op = "destroy"
)

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

type Decoder interface {
	Decode(ctx context.Context, plan providerkit.StackPlan, outputs auto.OutputMap) (providerkit.StackResult, error)
}

type Engine interface {
	Preview(ctx context.Context, setup Setup, op Op, report providerkit.Reporter) ([]providerkit.Change, error)

	Up(ctx context.Context, setup Setup, report providerkit.Reporter) (auto.OutputMap, error)

	Destroy(ctx context.Context, setup Setup, report providerkit.Reporter) error

	Outputs(ctx context.Context, setup Setup) (auto.OutputMap, error)
}

type Config struct {
	Access Access

	Program Program

	Parallel int

	Refresh func(ref providerkit.StackRef, op Op) bool

	Engine Engine
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
	Ref providerkit.StackRef

	Stack string

	Project workspace.Project

	Program pulumi.RunFunc

	EnvVars map[string]string

	Options []auto.LocalWorkspaceOption

	Config auto.ConfigMap

	Parallel int

	Refresh bool
}

func (a *Adapter) Workspace(plan providerkit.StackPlan) (Setup, error) {
	return a.workspace(plan, OpProvision)
}

func (a *Adapter) workspace(plan providerkit.StackPlan, op Op) (Setup, error) {
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
		Ref:      plan.Ref,
		Stack:    a.StackName(plan.Ref),
		Project:  project,
		Program:  program,
		EnvVars:  env,
		Parallel: a.parallel(),
		Refresh:  a.refreshes(plan.Ref, op),
		Options: []auto.LocalWorkspaceOption{
			auto.Project(project),
			auto.EnvVars(env),
			auto.Program(program),
			auto.SecretsProvider("passphrase"),
		},
	}, nil
}

func (a *Adapter) parallel() int {
	if a.config.Parallel > 0 {
		return a.config.Parallel
	}
	return DefaultParallel
}

func (a *Adapter) refreshes(ref providerkit.StackRef, op Op) bool {
	return a.config.Refresh != nil && a.config.Refresh(ref, op)
}

func (a *Adapter) env() map[string]string {
	env := map[string]string{
		passphraseEnvVar:           a.config.Access.Passphrase,
		backendEnvVar:              a.config.Access.BackendURL,
		"PULUMI_SKIP_CHECKPOINTS":  "true",
		"PULUMI_SKIP_UPDATE_CHECK": "true",
	}
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

func (a *Adapter) setup(ctx context.Context, plan providerkit.StackPlan, op Op, report providerkit.Reporter) (Setup, error) {
	setup, err := a.workspace(plan, op)
	if err != nil {
		return Setup{}, err
	}
	if setup.Config, err = a.Stack(ctx, plan); err != nil {
		return Setup{}, err
	}
	if a.config.Engine == nil {
		command, err := pinned.install(ctx, report)
		if err != nil {
			return Setup{}, err
		}
		setup.Options = append(setup.Options, auto.Pulumi(command))
	}
	return setup, nil
}

func (a *Adapter) engine() Engine {
	if a.config.Engine != nil {
		return a.config.Engine
	}
	return autoEngine{}
}

func (a *Adapter) Preview(ctx context.Context, plan providerkit.StackPlan, report providerkit.Reporter) (providerkit.Plan, error) {
	return a.preview(ctx, plan, OpProvision, report)
}

func (a *Adapter) PreviewDestroy(ctx context.Context, ref providerkit.StackRef, report providerkit.Reporter) (providerkit.Plan, error) {
	return a.preview(ctx, providerkit.StackPlan{Ref: ref}, OpDestroy, report)
}

func (a *Adapter) preview(ctx context.Context, plan providerkit.StackPlan, op Op, report providerkit.Reporter) (providerkit.Plan, error) {
	setup, err := a.setup(ctx, plan, op, report)
	if err != nil {
		return providerkit.Plan{}, err
	}
	changes, err := a.engine().Preview(ctx, setup, op, report)
	if err != nil {
		return providerkit.Plan{}, busy(err, setup)
	}
	if len(changes) == 0 {
		return providerkit.Plan{}, nil
	}
	group := providerkit.ChangeGroup{
		Kind:    providerkit.StackGroupKind,
		Name:    setup.Stack,
		Changes: changes,
	}
	group.Action, group.Reason = providerkit.RollUp(changes)
	return providerkit.Plan{Groups: []providerkit.ChangeGroup{group}}, nil
}

const stackResourceType = "pulumi:pulumi:Stack"

func planRows(mutations, standing []apitype.StepEventMetadata) []providerkit.Change {
	rows := make(map[string]providerkit.Change, len(mutations)+len(standing))
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
		rows[step.URN] = row(step, providerkit.ActionKeep)
	}
	changes := make([]providerkit.Change, 0, len(rows))
	for _, urn := range slices.Sorted(maps.Keys(rows)) {
		changes = append(changes, rows[urn])
	}
	return changes
}

func row(step apitype.StepEventMetadata, action providerkit.ChangeAction) providerkit.Change {
	return providerkit.Change{
		Kind:   capIdentifier(step.Type),
		Name:   resourceNameFromURN(step.URN),
		Action: action,
	}
}

func plannedAction(op apitype.OpType) (providerkit.ChangeAction, bool) {
	switch op {
	case apitype.OpCreate, apitype.OpCreateReplacement, apitype.OpImport:
		return providerkit.ActionCreate, true
	case apitype.OpUpdate:
		return providerkit.ActionUpdate, true
	case apitype.OpReplace, apitype.OpImportReplacement:
		return providerkit.ActionReplace, true
	case apitype.OpDelete, apitype.OpDeleteReplaced:
		return providerkit.ActionDelete, true
	default:
		return "", false
	}
}

func (a *Adapter) Run(ctx context.Context, plan providerkit.StackPlan, report providerkit.Reporter) (providerkit.StackResult, error) {
	setup, err := a.setup(ctx, plan, OpProvision, report)
	if err != nil {
		return providerkit.StackResult{}, err
	}
	outputs, err := a.engine().Up(ctx, setup, report)
	if err != nil {
		return providerkit.StackResult{}, busy(err, setup)
	}
	decoder, decodes := a.config.Program.(Decoder)
	if !decodes {
		return providerkit.StackResult{}, nil
	}
	return decoder.Decode(ctx, plan, outputs)
}

func (a *Adapter) Destroy(ctx context.Context, ref providerkit.StackRef, report providerkit.Reporter) error {
	setup, err := a.setup(ctx, providerkit.StackPlan{Ref: ref}, OpDestroy, report)
	if err != nil {
		return err
	}
	if err := a.engine().Destroy(ctx, setup, report); err != nil {
		return busy(err, setup)
	}
	return nil
}

func (a *Adapter) Outputs(ctx context.Context, ref providerkit.StackRef, report providerkit.Reporter) (auto.OutputMap, error) {
	setup, err := a.setup(ctx, providerkit.StackPlan{Ref: ref}, OpProvision, report)
	if err != nil {
		return nil, err
	}
	return a.engine().Outputs(ctx, setup)
}

const lockedMessage = "the stack is currently locked"

func busy(err error, setup Setup) error {
	if err == nil || !strings.Contains(err.Error(), lockedMessage) {
		return err
	}
	return providerkit.Refuse(providerkit.CodeBusy,
		"%s is locked by a run that is either still working or was killed."+
			"\n\nconfirm no deploy or teardown is running against this stack, then release it with:"+
			"\n  PULUMI_BACKEND_URL=%s PULUMI_CONFIG_PASSPHRASE=<the account passphrase> pulumi cancel --stack %s"+
			"\nand run this again",
		setup.Stack, setup.Project.Backend.URL, setup.Stack)
}

type autoEngine struct{}

func (autoEngine) Preview(ctx context.Context, setup Setup, op Op, report providerkit.Reporter) ([]providerkit.Change, error) {
	stack, err := auto.UpsertStackInlineSource(ctx, setup.Stack, string(setup.Project.Name), setup.Program, setup.Options...)
	if err != nil {
		return nil, fmt.Errorf("prepare stack %s: %w", setup.Stack, err)
	}
	if err := applyConfig(ctx, stack, setup.Config); err != nil {
		return nil, err
	}

	engineEvents := make(chan events.EngineEvent, 256)
	rows := drainRows(engineEvents)
	if report != nil {
		report.Say("Working out what would change")
	}

	if op == OpDestroy {
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

func drainRows(engineEvents <-chan events.EngineEvent) <-chan []providerkit.Change {
	drained := make(chan []providerkit.Change, 1)
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

func awaitRows(drained <-chan []providerkit.Change, grace time.Duration) ([]providerkit.Change, error) {
	select {
	case rows := <-drained:
		return rows, nil
	case <-time.After(grace):
		return nil, fmt.Errorf(
			"the engine's plan rows did not drain within %s, and the rows that did arrive would read as a plan doing less than the run would do", grace)
	}
}

func (autoEngine) Up(ctx context.Context, setup Setup, report providerkit.Reporter) (auto.OutputMap, error) {
	stack, err := auto.UpsertStackInlineSource(ctx, setup.Stack, string(setup.Project.Name), setup.Program, setup.Options...)
	if err != nil {
		return nil, fmt.Errorf("prepare stack %s: %w", setup.Stack, err)
	}
	if err := applyConfig(ctx, stack, setup.Config); err != nil {
		return nil, err
	}

	lines := detailWriter(report)
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
	reportTrace(report, trace, upErr)

	if upErr != nil {
		return nil, fmt.Errorf("provision stack %s: %w", setup.Stack, upErr)
	}
	return res.Outputs, nil
}

func (autoEngine) Destroy(ctx context.Context, setup Setup, report providerkit.Reporter) error {
	stack, err := auto.SelectStackInlineSource(ctx, setup.Stack, string(setup.Project.Name), nil, setup.Options...)
	if auto.IsSelectStack404Error(err) {
		if report != nil {
			report.Say("No stack " + setup.Stack + " to destroy")
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("select stack %s: %w", setup.Stack, err)
	}

	if report != nil {
		report.Say("Destroying resources (this can take several minutes)")
	}
	lines := detailWriter(report)
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

func (autoEngine) Outputs(ctx context.Context, setup Setup) (auto.OutputMap, error) {
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
