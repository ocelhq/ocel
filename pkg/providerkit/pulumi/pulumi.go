// Package pulumi adapts the Pulumi automation API to providerkit's Releaser
// port. It owns everything about driving the engine — the CLI pin and its
// install, the workspace and its env, parallelism, stack locks, the progress
// stream and the engine trace — so that a vendor writes only the two halves that
// are genuinely its own: a program that declares resources, and a decoder that
// reads the outputs back as kit values.
//
// Nothing in here knows a cloud. The one place a vendor's vocabulary could leak
// — the config keys the engine wants stamped on a stack before it runs — is a
// Program's own answer through Configurer, so the adapter stamps keys it cannot
// name.
package pulumi

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"sync"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optdestroy"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/config"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

// Access is the workspace triple plus the env the vendor's programs need. It is
// resolved per stack rather than held on the adapter because a backend and its
// passphrase belong to an account, and which account a stack belongs to is the
// kit's answer, not the engine's.
type Access struct {
	BackendURL string
	Passphrase string

	// Project is the engine's project name, which is not the kit's project: it
	// is the namespace the backend files state under.
	Project string

	// Env is what the vendor's own SDKs read out of the process — a region, a
	// profile, a token. The adapter adds the PULUMI_* variables itself so a
	// vendor cannot half-configure the engine by accident.
	Env map[string]string
}

// Program is the vendor's half of a stack: the declaration and the reading-back.
// Splitting it here is what keeps the adapter free of resource types — Run sees
// a plan and an engine context, Decode sees outputs, and neither hands the
// adapter anything it would have to interpret.
type Program interface {
	Run(ctx *pulumi.Context, plan providerkit.StackPlan) error
	Decode(outputs auto.OutputMap) (providerkit.StackResult, error)
}

// Configurer is optional on a Program: stack config keys to stamp before the run.
// This is how a vendor gets a setting like default tags onto every resource
// without the adapter ever learning the key's namespace.
type Configurer interface {
	Config(plan providerkit.StackPlan) map[string]string
}

// Config is everything the adapter needs to serve a Releaser. The two functions
// are lookups rather than values because a Releaser outlives any one stack and
// both answers vary by which stack is being asked about.
type Config struct {
	// Version pins the CLI. Zero means DefaultVersion; a pin exists at all so a
	// deploy is not at the mercy of whatever the machine last downloaded.
	Version string

	// Root is where that CLI is installed. Zero means a per-version directory
	// under the user's home, so two pins can coexist.
	Root string

	Access  func(ctx context.Context, ref providerkit.StackRef) (Access, error)
	Program func(ref providerkit.StackRef) (Program, error)
}

// New builds the adapter a vendor composes into its root: the root's Releases()
// returns it, and the root's own Inspect forwards to it. It returns the concrete
// type rather than the port because optional sets are asserted on the root and
// never on a port — a root that handed back only a Releaser could not offer
// StackInspector without hiding where the answer comes from.
//
// The CLI installs lazily, on the first call that needs it, so constructing a
// provider costs nothing and a user who never deploys never pays for a download.
func New(cfg Config) *Adapter {
	return &Adapter{cfg: cfg, realized: map[string]bool{}}
}

var (
	_ providerkit.Releaser       = (*Adapter)(nil)
	_ providerkit.StackInspector = (*Adapter)(nil)
)

// Adapter drives one engine on behalf of one provider. It is a value rather than
// a set of functions because the CLI install and the set of stacks this process
// brought up are both state a second stack in the same run should reuse.
type Adapter struct {
	cfg Config

	once   sync.Once
	cmd    auto.PulumiCommand
	cmdErr error

	mu       sync.Mutex
	realized map[string]bool
}

func (a *Adapter) Provision(ctx context.Context, plan providerkit.StackPlan, report providerkit.Reporter) (providerkit.StackResult, error) {
	report = reporterOr(report)
	access, opts, err := a.workspace(ctx, plan.Ref, report)
	if err != nil {
		return providerkit.StackResult{}, err
	}
	prog, err := a.program(plan.Ref)
	if err != nil {
		return providerkit.StackResult{}, err
	}

	name := plan.Ref.Name.String()
	stack, err := auto.UpsertStackInlineSource(ctx, name, access.Project, func(c *pulumi.Context) error {
		return prog.Run(c, plan)
	}, opts...)
	if err != nil {
		if busy := busyRefusal(err, access, name); busy != nil {
			return providerkit.StackResult{}, busy
		}
		return providerkit.StackResult{}, fmt.Errorf("provision stack %s: %w", name, err)
	}

	if c, ok := prog.(Configurer); ok {
		if err := stampConfig(ctx, stack, c.Config(plan)); err != nil {
			return providerkit.StackResult{}, fmt.Errorf("provision stack %s: %w", name, err)
		}
	}
	a.markRealized(plan.Ref)

	res, err := a.up(ctx, stack, report)
	if err != nil {
		if busy := busyRefusal(err, access, name); busy != nil {
			return providerkit.StackResult{}, busy
		}
		return providerkit.StackResult{}, fmt.Errorf("provision stack %s: %w", name, err)
	}
	return prog.Decode(res.Outputs)
}

func (a *Adapter) Destroy(ctx context.Context, ref providerkit.StackRef, report providerkit.Reporter) error {
	report = reporterOr(report)
	access, opts, err := a.workspace(ctx, ref, report)
	if err != nil {
		return err
	}

	name := ref.Name.String()
	stack, err := auto.SelectStackInlineSource(ctx, name, access.Project, nil, opts...)
	if auto.IsSelectStack404Error(err) {
		report.Say("No stack " + name + " to destroy")
		return nil
	}
	if err != nil {
		return fmt.Errorf("select stack %s: %w", name, err)
	}

	lines := lineWriter(report.Detail)
	opt := []optdestroy.Option{optdestroy.ProgressStreams(lines)}
	if !a.realizedHere(ref) {
		opt = append(opt, optdestroy.Refresh())
	}
	_, err = stack.Destroy(ctx, opt...)
	lines.Flush()
	if err != nil {
		if busy := busyRefusal(err, access, name); busy != nil {
			return busy
		}
		return fmt.Errorf("destroy stack %s: %w", name, err)
	}

	if err := stack.Workspace().RemoveStack(ctx, name); err != nil {
		return fmt.Errorf("remove stack %s: %w", name, err)
	}
	return nil
}

func (a *Adapter) Inspect(ctx context.Context, ref providerkit.StackRef) (providerkit.StackState, error) {
	report := reporterOr(nil)
	access, opts, err := a.workspace(ctx, ref, report)
	if err != nil {
		return providerkit.StackState{}, err
	}
	prog, err := a.program(ref)
	if err != nil {
		return providerkit.StackState{}, err
	}

	name := ref.Name.String()
	stack, err := auto.SelectStackInlineSource(ctx, name, access.Project, nil, opts...)
	if auto.IsSelectStack404Error(err) {
		return providerkit.StackState{}, nil
	}
	if err != nil {
		return providerkit.StackState{}, fmt.Errorf("select stack %s: %w", name, err)
	}

	outputs, err := stack.Outputs(ctx)
	if err != nil {
		return providerkit.StackState{}, fmt.Errorf("read stack %s: %w", name, err)
	}
	result, err := prog.Decode(outputs)
	if err != nil {
		return providerkit.StackState{}, err
	}
	return providerkit.StackState{Present: true, Result: result}, nil
}

func (a *Adapter) program(ref providerkit.StackRef) (Program, error) {
	if a.cfg.Program == nil {
		return nil, providerkit.Refuse(providerkit.CodeNotReady, "this provider declares no Pulumi program")
	}
	prog, err := a.cfg.Program(ref)
	if err != nil {
		return nil, err
	}
	if prog == nil {
		return nil, providerkit.Refuse(providerkit.CodeNotReady, "this provider declares no Pulumi program for stack %s", ref.Name)
	}
	return prog, nil
}

// workspace resolves the access for one stack and turns it into the options
// every automation-API call takes. Both halves are here because an incomplete
// access is a refusal, not a crash three layers down inside the CLI.
func (a *Adapter) workspace(ctx context.Context, ref providerkit.StackRef, report providerkit.Reporter) (Access, []auto.LocalWorkspaceOption, error) {
	if a.cfg.Access == nil {
		return Access{}, nil, providerkit.Refuse(providerkit.CodeNotReady, "this provider resolves no Pulumi backend")
	}
	access, err := a.cfg.Access(ctx, ref)
	if err != nil {
		return Access{}, nil, err
	}
	if missing := access.missing(); len(missing) > 0 {
		return Access{}, nil, providerkit.Refuse(providerkit.CodeNotReady,
			"the account this stack belongs to has no %s; bootstrap it and retry", strings.Join(missing, " and no "))
	}

	cmd, err := a.command(ctx, report)
	if err != nil {
		return Access{}, nil, err
	}
	return access, []auto.LocalWorkspaceOption{
		auto.Pulumi(cmd),
		auto.SecretsProvider("passphrase"),
		auto.EnvVars(access.env()),
	}, nil
}

func (a Access) missing() []string {
	var missing []string
	if a.BackendURL == "" {
		missing = append(missing, "state backend")
	}
	if a.Passphrase == "" {
		missing = append(missing, "state passphrase")
	}
	if a.Project == "" {
		missing = append(missing, "state project name")
	}
	return missing
}

// env layers the engine's own variables over the vendor's, so a vendor cannot
// point the engine at a different backend by putting one in Env.
func (a Access) env() map[string]string {
	env := make(map[string]string, len(a.Env)+4)
	maps.Copy(env, a.Env)
	env["PULUMI_BACKEND_URL"] = a.BackendURL
	env["PULUMI_CONFIG_PASSPHRASE"] = a.Passphrase
	env["PULUMI_SKIP_CHECKPOINTS"] = "true"
	env["PULUMI_SKIP_UPDATE_CHECK"] = "true"
	return env
}

// stampConfig writes a Program's settings onto the stack. A value that parses as
// a JSON object or array is stored as one, because a flattened string is not the
// same value to the engine and a setting like a tag map has to survive as a map.
func stampConfig(ctx context.Context, stack auto.Stack, settings map[string]string) error {
	if len(settings) == 0 {
		return nil
	}
	ws := stack.Workspace()
	current, err := ws.StackSettings(ctx, stack.Name())
	if err != nil {
		current = &workspace.ProjectStack{}
	}
	if current.Config == nil {
		current.Config = config.Map{}
	}
	for raw, value := range settings {
		key, err := config.ParseKey(raw)
		if err != nil {
			return fmt.Errorf("read config key %s: %w", raw, err)
		}
		current.Config[key] = configValue(value)
	}
	if err := ws.SaveStackSettings(ctx, stack.Name(), current); err != nil {
		return fmt.Errorf("stamp config on %s: %w", stack.Name(), err)
	}
	return nil
}

func configValue(value string) config.Value {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		if json.Valid([]byte(trimmed)) {
			return config.NewObjectValue(trimmed)
		}
	}
	return config.NewValue(value)
}

// realizedHere records that this process brought a stack up. A stack the engine
// just wrote needs no refresh before a destroy, and a refresh is the slowest
// thing a teardown does.
func (a *Adapter) markRealized(ref providerkit.StackRef) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.realized[realizedKey(ref)] = true
}

func (a *Adapter) realizedHere(ref providerkit.StackRef) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.realized[realizedKey(ref)]
}

func realizedKey(ref providerkit.StackRef) string {
	return ref.Project + "/" + ref.Name.String()
}

const lockedMarker = "the stack is currently locked"

// busyRefusal turns a lock into an answer the user can act on. It is a refusal
// and not a failure because nothing is wrong with the request, and it carries
// the release recipe because the recipe is otherwise undiscoverable.
func busyRefusal(err error, access Access, name string) error {
	if err == nil {
		return nil
	}
	if !auto.IsConcurrentUpdateError(err) && !strings.Contains(err.Error(), lockedMarker) {
		return nil
	}
	return providerkit.Refuse(providerkit.CodeBusy,
		"stack %s is locked by another run.\n\n"+
			"this lock outlives a run that was killed rather than one still working.\n"+
			"confirm no deploy or teardown is running against this stack, then release it with:\n"+
			"  PULUMI_BACKEND_URL=%s PULUMI_CONFIG_PASSPHRASE=<the account passphrase> pulumi cancel --stack %s\n"+
			"and re-run", name, access.BackendURL, name)
}
