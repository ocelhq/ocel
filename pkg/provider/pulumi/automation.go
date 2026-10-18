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
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
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
	Preview(ctx context.Context, setup WorkspaceSpec, op Operation, progress progress.Log) ([]provider.Change, error)

	Up(ctx context.Context, setup WorkspaceSpec, progress progress.Log) (auto.OutputMap, error)

	Destroy(ctx context.Context, setup WorkspaceSpec, progress progress.Log) error

	Outputs(ctx context.Context, setup WorkspaceSpec) (auto.OutputMap, error)

	Unlock(ctx context.Context, setup WorkspaceSpec) error
}

type Config[S any] struct {
	Backend Backend

	Program func(ctx *pulumi.Context, work S) error

	Configure func(ctx context.Context, work S) (auto.ConfigMap, error)

	Secrets func(work S) []string

	Parallel int

	Refresh func(ref provider.StackRef, op Operation) bool

	Engine Engine

	Plugins []Plugin
}

type Automation[S any] struct {
	config  Config[S]
	plugins *hostedPlugins
}

func New[S any](config Config[S]) *Automation[S] {
	return &Automation[S]{config: config, plugins: &hostedPlugins{declared: config.Plugins}}
}

func (a *Automation[S]) Backend() Backend { return a.config.Backend }

func (a *Automation[S]) StackName(ref provider.StackRef) string { return ref.Name.String() }

func (a *Automation[S]) ProjectName(ref provider.StackRef) string {
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

	EnvVariables map[string]string

	Options []auto.LocalWorkspaceOption

	Config auto.ConfigMap

	Secrets []string

	Parallel int

	Refresh bool
}

func (a *Automation[S]) Workspace(ref provider.StackRef, work S) (WorkspaceSpec, error) {
	return a.workspace(ref, work, OperationProvision)
}

func (a *Automation[S]) workspace(ref provider.StackRef, work S, op Operation) (WorkspaceSpec, error) {
	backend := a.config.Backend
	switch {
	case backend.URL == "":
		return WorkspaceSpec{}, refusal.Refuse(refusal.CodeNotReady,
			"this provider names no state backend, and an engine run has nowhere to keep %s's state", ref.Name)
	case backend.Passphrase == "":
		return WorkspaceSpec{}, refusal.Refuse(refusal.CodeNotReady,
			"this provider names no state passphrase, and %s's state would be written unsealed", ref.Name)
	case a.config.Program == nil:
		return WorkspaceSpec{}, refusal.Refuse(refusal.CodeNotReady,
			"this automation has no program, so there is nothing for the engine to run over %s", ref.Name)
	}

	project := workspace.Project{
		Name:    tokens.PackageName(a.ProjectName(ref)),
		Runtime: workspace.NewProjectRuntimeInfo("go", nil),
		Backend: &workspace.ProjectBackend{URL: backend.URL},
	}
	env, err := a.env()
	if err != nil {
		return WorkspaceSpec{}, err
	}
	program := func(ctx *pulumi.Context) error { return a.config.Program(ctx, work) }

	return WorkspaceSpec{
		Ref:          ref,
		Stack:        a.StackName(ref),
		Project:      project,
		Program:      program,
		EnvVariables: env,
		Parallel:     a.parallel(),
		Refresh:      a.refreshes(ref, op),
		Options: []auto.LocalWorkspaceOption{
			auto.Project(project),
			auto.EnvVars(env),
			auto.Program(program),
			auto.SecretsProvider("passphrase"),
		},
	}, nil
}

func (a *Automation[S]) parallel() int {
	if a.config.Parallel > 0 {
		return a.config.Parallel
	}
	return DefaultParallel
}

func (a *Automation[S]) refreshes(ref provider.StackRef, op Operation) bool {
	return a.config.Refresh != nil && a.config.Refresh(ref, op)
}

func (a *Automation[S]) env() (map[string]string, error) {
	env := map[string]string{
		passphraseEnvVar: a.config.Backend.Passphrase,
		backendEnvVar:    a.config.Backend.URL,
		// TODO: revisit: checkpoints are skipped for speed, so a run interrupted before it closes can lose the state of what it created.
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

func (a *Automation[S]) StackConfig(ctx context.Context, ref provider.StackRef, work S) (auto.ConfigMap, error) {
	if _, err := a.Workspace(ref, work); err != nil {
		return nil, err
	}
	if a.config.Configure == nil {
		return auto.ConfigMap{}, nil
	}
	return a.config.Configure(ctx, work)
}

func (a *Automation[S]) setup(ctx context.Context, ref provider.StackRef, work S, op Operation, progress progress.Log) (WorkspaceSpec, error) {
	setup, err := a.workspace(ref, work, op)
	if err != nil {
		return WorkspaceSpec{}, err
	}
	if setup.Config, err = a.StackConfig(ctx, ref, work); err != nil {
		return WorkspaceSpec{}, err
	}
	if err := refuseSecretConfig(setup.Config, setup.Stack); err != nil {
		return WorkspaceSpec{}, err
	}
	setup.Secrets = a.secrets(work)
	if a.config.Engine == nil {
		command, err := pinned.install(ctx, progress)
		if err != nil {
			return WorkspaceSpec{}, err
		}
		setup.Options = append(setup.Options, auto.Pulumi(command))
	}
	return setup, nil
}

func (a *Automation[S]) engine() Engine {
	if a.config.Engine != nil {
		return a.config.Engine
	}
	return autoEngine{}
}

func (a *Automation[S]) Preview(ctx context.Context, ref provider.StackRef, work S, progress progress.Log) (provider.Plan, error) {
	return a.preview(ctx, ref, work, OperationProvision, progress)
}

func (a *Automation[S]) PreviewDestroy(ctx context.Context, ref provider.StackRef, progress progress.Log) (provider.Plan, error) {
	var none S
	return a.preview(ctx, ref, none, OperationDestroy, progress)
}

func (a *Automation[S]) preview(ctx context.Context, ref provider.StackRef, work S, op Operation, progress progress.Log) (provider.Plan, error) {
	setup, err := a.setup(ctx, ref, work, op, progress)
	if err != nil {
		return provider.Plan{}, err
	}
	if progress != nil {
		planned := "the changes to"
		if op == OperationDestroy {
			planned = "the teardown of"
		}
		progress.Say("Planning " + planned + " stack " + setup.Stack)
	}
	changes, err := runReleasingStaleLock(ctx, a, setup, progress, func() ([]provider.Change, error) {
		return a.engine().Preview(ctx, setup, op, progress)
	})
	if err != nil {
		return provider.Plan{}, masked(busy(err, setup), setup.Secrets)
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

func planRows(mutations, unchanged []apitype.StepEventMetadata) []provider.Change {
	rows := make(map[string]provider.Change, len(mutations)+len(unchanged))
	for _, step := range mutations {
		action, mutates := plannedAction(step.Op)
		if !mutates || step.Type == stackResourceType {
			continue
		}
		rows[step.URN] = row(step, action)
	}
	for _, step := range unchanged {
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

func (a *Automation[S]) Run(ctx context.Context, ref provider.StackRef, work S, progress progress.Log) (auto.OutputMap, error) {
	setup, err := a.setup(ctx, ref, work, OperationProvision, progress)
	if err != nil {
		return nil, err
	}
	outputs, err := runReleasingStaleLock(ctx, a, setup, progress, func() (auto.OutputMap, error) {
		return a.engine().Up(ctx, setup, progress)
	})
	if err != nil {
		return nil, masked(busy(err, setup), setup.Secrets)
	}
	return outputs, nil
}

func (a *Automation[S]) Destroy(ctx context.Context, ref provider.StackRef, progress progress.Log) error {
	var none S
	setup, err := a.setup(ctx, ref, none, OperationDestroy, progress)
	if err != nil {
		return err
	}
	_, err = runReleasingStaleLock(ctx, a, setup, progress, func() (struct{}, error) {
		return struct{}{}, a.engine().Destroy(ctx, setup, progress)
	})
	if err != nil {
		return masked(busy(err, setup), setup.Secrets)
	}
	return nil
}

func (a *Automation[S]) Outputs(ctx context.Context, ref provider.StackRef, progress progress.Log) (auto.OutputMap, error) {
	var none S
	setup, err := a.setup(ctx, ref, none, OperationProvision, progress)
	if err != nil {
		return nil, err
	}
	outputs, err := a.engine().Outputs(ctx, setup)
	if err != nil {
		return nil, masked(err, setup.Secrets)
	}
	return outputs, nil
}

const lockedMessage = "the stack is currently locked"

func isLocked(err error) bool {
	return err != nil && strings.Contains(err.Error(), lockedMessage)
}

func busy(err error, setup WorkspaceSpec) error {
	if !isLocked(err) {
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

func (autoEngine) Preview(ctx context.Context, setup WorkspaceSpec, op Operation, progress progress.Log) ([]provider.Change, error) {
	if op == OperationDestroy {
		return previewDestroy(ctx, setup)
	}
	stack, err := auto.UpsertStackInlineSource(ctx, setup.Stack, string(setup.Project.Name), setup.Program, setup.Options...)
	if err != nil {
		return nil, fmt.Errorf("prepare stack %s: %w", setup.Stack, err)
	}
	if err := applyConfig(ctx, stack, setup.Config); err != nil {
		return nil, err
	}

	engineEvents := make(chan events.EngineEvent, 256)
	rows := drainRows(engineEvents)
	opts := []optpreview.Option{optpreview.EventStreams(engineEvents), optpreview.Parallel(setup.Parallel)}
	if setup.Refresh {
		opts = append(opts, optpreview.Refresh())
	}
	_, err = stack.Preview(ctx, opts...)
	return awaitPlan(rows, setup, err)
}

func previewDestroy(ctx context.Context, setup WorkspaceSpec) ([]provider.Change, error) {
	stack, err := auto.SelectStackInlineSource(ctx, setup.Stack, string(setup.Project.Name), nil, setup.Options...)
	if auto.IsSelectStack404Error(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select stack %s: %w", setup.Stack, err)
	}

	engineEvents := make(chan events.EngineEvent, 256)
	rows := drainRows(engineEvents)
	opts := []optdestroy.Option{optdestroy.EventStreams(engineEvents), optdestroy.Parallel(setup.Parallel)}
	if setup.Refresh {
		opts = append(opts, optdestroy.Refresh())
	}
	_, err = stack.PreviewDestroy(ctx, opts...)
	return awaitPlan(rows, setup, err)
}

func awaitPlan(rows <-chan []provider.Change, setup WorkspaceSpec, err error) ([]provider.Change, error) {
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
		var mutations, unchanged []apitype.StepEventMetadata
		for ev := range engineEvents {
			switch {
			case ev.ResourcePreEvent != nil:
				mutations = append(mutations, ev.ResourcePreEvent.Metadata)
			case ev.ResOutputsEvent != nil:
				unchanged = append(unchanged, ev.ResOutputsEvent.Metadata)
			}
		}
		drained <- planRows(mutations, unchanged)
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

func (autoEngine) Up(ctx context.Context, setup WorkspaceSpec, progress progress.Log) (auto.OutputMap, error) {
	stack, err := auto.UpsertStackInlineSource(ctx, setup.Stack, string(setup.Project.Name), setup.Program, setup.Options...)
	if err != nil {
		return nil, fmt.Errorf("prepare stack %s: %w", setup.Stack, err)
	}
	if err := applyConfig(ctx, stack, setup.Config); err != nil {
		return nil, err
	}

	var res auto.UpResult
	err = runReportingResourceOperations(setup, progress, func(engineEvents chan<- events.EngineEvent, lines *lineLog) error {
		opts := []optup.Option{optup.Parallel(setup.Parallel), optup.EventStreams(engineEvents)}
		if lines != nil {
			opts = append(opts, optup.ProgressStreams(lines))
		}
		if setup.Refresh {
			opts = append(opts, optup.Refresh())
		}
		var err error
		res, err = stack.Up(ctx, opts...)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("provision stack %s: %w", setup.Stack, err)
	}
	return res.Outputs, nil
}

func runReportingResourceOperations(setup WorkspaceSpec, progress progress.Log, run func(engineEvents chan<- events.EngineEvent, lines *lineLog) error) error {
	lines := engineLines(progress)
	engineEvents := make(chan events.EngineEvent, 256)
	traced := drainTrace(engineEvents, resourceLatencyOutlierThreshold, setup.Secrets)

	start := time.Now()
	err := run(engineEvents, lines)
	end := time.Now()
	lines.Flush()

	trace := awaitTrace(traced, engineDrainGrace)
	if trace.Start.IsZero() {
		trace.Start, trace.End = start, end
	}
	reportTrace(progress, trace, err)
	return err
}

func (autoEngine) Destroy(ctx context.Context, setup WorkspaceSpec, progress progress.Log) error {
	stack, err := auto.SelectStackInlineSource(ctx, setup.Stack, string(setup.Project.Name), nil, setup.Options...)
	if auto.IsSelectStack404Error(err) {
		if progress != nil {
			progress.Say("Nothing to destroy: stack " + setup.Stack + " does not exist")
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("select stack %s: %w", setup.Stack, err)
	}

	if progress != nil {
		progress.Say("Destroying every resource in stack " + setup.Stack + ", which can take several minutes")
	}
	err = runReportingResourceOperations(setup, progress, func(engineEvents chan<- events.EngineEvent, lines *lineLog) error {
		opts := []optdestroy.Option{optdestroy.Parallel(setup.Parallel), optdestroy.EventStreams(engineEvents)}
		if lines != nil {
			opts = append(opts, optdestroy.ProgressStreams(lines))
		}
		if setup.Refresh {
			opts = append(opts, optdestroy.Refresh())
		}
		_, err := stack.Destroy(ctx, opts...)
		return err
	})
	if err != nil {
		return fmt.Errorf("destroy stack %s: %w", setup.Stack, err)
	}

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

func (autoEngine) Unlock(ctx context.Context, setup WorkspaceSpec) error {
	stack, err := auto.SelectStackInlineSource(ctx, setup.Stack, string(setup.Project.Name), nil, setup.Options...)
	if auto.IsSelectStack404Error(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("select stack %s: %w", setup.Stack, err)
	}
	return stack.Cancel(ctx)
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

func refuseSecretConfig(values auto.ConfigMap, stack string) error {
	for _, name := range slices.Sorted(maps.Keys(values)) {
		if values[name].Secret {
			return fmt.Errorf("configure %s: %s is marked secret, and this engine writes stack config as plaintext; hand the value to the program instead", stack, name)
		}
	}
	return nil
}

func (a *Automation[S]) secrets(work S) []string {
	secrets := []string{a.config.Backend.Passphrase}
	if a.config.Secrets != nil {
		secrets = append(secrets, a.config.Secrets(work)...)
	}
	return slices.DeleteFunc(secrets, func(secret string) bool { return secret == "" })
}

type maskedError struct {
	err     error
	secrets []string
}

func masked(err error, secrets []string) error {
	return &maskedError{err: err, secrets: secrets}
}

func (e *maskedError) Error() string { return maskSecrets(e.err.Error(), e.secrets) }

func (e *maskedError) Unwrap() error { return e.err }

func maskSecrets(text string, secrets []string) string {
	longestFirst := slices.SortedFunc(slices.Values(secrets), func(a, b string) int { return len(b) - len(a) })
	for _, secret := range longestFirst {
		text = strings.ReplaceAll(text, secret, redactedSecret)
	}
	return text
}

func configValue(value auto.ConfigValue) config.Value {
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
