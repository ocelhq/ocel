package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/attribution"
	"github.com/ocelhq/ocel/cli/internal/clientenv"
	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/deploycollector"
	"github.com/ocelhq/ocel/cli/internal/deployresult"
	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	"github.com/ocelhq/ocel/cli/internal/servicemap"
	"github.com/ocelhq/ocel/cli/node"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	envv1 "github.com/ocelhq/ocel/pkg/proto/env/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

var deployReadyTimeout time.Duration

const noBrowserEnvVar = "OCEL_NO_BROWSER"

func (d deps) canOpenVarsUI(stdin io.Reader, noUI bool) bool {
	if noUI || os.Getenv(noBrowserEnvVar) != "" {
		return false
	}
	return d.stdinIsTerminal(stdin)
}

const noUIFlagUsage = "Never pause to open the variables UI; fail on a missing or invalid variable instead"

type deployOptions struct {
	yes      bool
	tag      string
	prebuilt bool
	noUI     bool
}

var deployOpts deployOptions

var deployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "Deploy your project to its configured cloud provider",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}

		ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
		defer stop()

		return runDeploy(ctx, defaultDeps(), cwd, deployOpts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	},
}

func init() {
	deployCmd.Flags().BoolVarP(&deployOpts.yes, "yes", "y", false, "Skip the confirmation prompt")
	deployCmd.Flags().StringVar(&deployOpts.tag, "tag", "", "Stamp this deploy with an immutable label to roll back to later (`ocel rollback --tag <tag>`)")
	deployCmd.Flags().BoolVar(&deployOpts.prebuilt, "prebuilt", false, prebuiltFlagUsage)
	deployCmd.Flags().BoolVar(&deployOpts.noUI, "no-ui", false, noUIFlagUsage)
}

const prebuiltFlagUsage = "Deploy the existing .ocel/output instead of building the apps first (produce it with ocel build)"

func runDeploy(ctx context.Context, d deps, cwd string, opts deployOptions, stdout, stderr io.Writer, stdin io.Reader) error {
	if err := validateTag(opts.tag); err != nil {
		return err
	}

	cfg, err := projectconfig.Resolve(ctx, cwd)
	if err != nil {
		return err
	}

	if err := node.Ensure(cfg.Dir); err != nil {
		return err
	}

	provider, err := cfg.RequireProvider()
	if err != nil {
		return err
	}

	if err := deployresult.Clear(cfg.Dir); err != nil {
		return err
	}
	if err := servicemap.Clear(cfg.Dir); err != nil {
		return err
	}

	ctx, run, err := startRun(ctx, cfg, "ocel deploy")
	if err != nil {
		return err
	}
	defer run.Close()

	ui := deployui.New(stdout, run, sessionFormat(), verboseEnabled())
	defer ui.Close()

	provW := ui.BuildWriter()
	err = runProviderSession(ctx, d, cfg, provider, provW, provW, func(runner *providerrunner.Runner) error {
		willConfirm := !opts.yes && d.stdinIsTerminal(stdin)
		knownSlugs, err := preflightDeploy(ctx, d, runner, provider, cfg, willConfirm, stdout)
		if err != nil {
			return err
		}

		if willConfirm {
			proceed, err := confirmDeploy(ctx, cfg.Slug, provider.Package, knownSlugs, stdout, stdin)
			if err != nil {
				return err
			}
			if !proceed {
				fmt.Fprintln(stdout, "Aborted.")
				return nil
			}
		}

		ui.Building()
		recovery := gateRecovery{
			deps:     d,
			cfg:      cfg,
			provider: provider,
			runner:   runner,
			newGate: func() *envgate.Gate {
				return envgate.New(runnerValues{
					runner:  runner,
					options: []byte(provider.Options),
					slug:    cfg.Slug,
					class:   deploymentsv1.Environment_CLASS_PRODUCTION,
				}, envScope(cfg, false, ""))
			},
			ui:      ui,
			stdout:  stdout,
			enabled: d.canOpenVarsUI(stdin, opts.noUI),
		}
		manifest, err := recovery.buildManifest(ctx, opts.prebuilt)
		if err != nil {
			return err
		}
		if manifest == nil {
			ui.Finish("Nothing to deploy")
			return nil
		}
		ui.BuildOK()

		env := &deploymentsv1.Environment{
			Class:     deploymentsv1.Environment_CLASS_PRODUCTION,
			Lifecycle: deploymentsv1.Environment_LIFECYCLE_UNSPECIFIED,
		}
		req := &deploymentsv1.DeployRequest{
			Manifest:        manifest,
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			Environment:     env,
			Tag:             opts.tag,
		}

		var links []*linksv1.Link
		var functions []*deploymentsv1.FunctionOutput
		var appURLs []string
		var promotionID string
		onEvent := func(ev *deploymentsv1.DeployEvent) {
			ui.Event(ev)
			if res := ev.GetResult(); res != nil {
				links = res.GetLinks()
				functions = res.GetFunctions()
				appURLs = res.GetAppUrls()
				promotionID = res.GetPromotionId()
			}
		}
		if err := runner.Deploy(ctx, req, onEvent); err != nil {
			return err
		}

		if err := recordDeployResult(cfg, manifest, env, opts.tag, promotionID, appURLs); err != nil {
			return err
		}
		if err := publishServiceMap(cfg, manifest, env, opts.tag, promotionID, links); err != nil {
			return err
		}
		ui.Deployed("Deployed", appURLs, links, functions)
		return nil
	})
	if err != nil {
		return failSession(ctx, ui, err)
	}
	return nil
}

func collectAndBuildManifest(ctx context.Context, d deps, cfg *projectconfig.Config, gate *envgate.Gate, prebuilt bool, ui *deployui.Session) (*deploymentsv1.Manifest, error) {
	buildOut := ui.BuildWriter()

	captured := &boundedCapture{}
	tee := io.MultiWriter(buildOut, captured)
	resources, err := deploycollector.Collect(ctx, cfg, gate, tee, tee)
	if err != nil {
		return nil, captured.annotate(err)
	}

	usages, err := attribution.Compute(cfg.Dir, toAttributionApps(cfg.Apps), toAttributionDeclarations(resources))
	if err != nil {
		return nil, err
	}

	warnings, err := envgate.Lint(gate.Definitions(), envApps(cfg), filepath.Join(cfg.Dir, projectconfig.ConfigFileName))
	if err != nil {
		return nil, err
	}
	for _, warning := range warnings {
		ui.Diagnostic("warning: " + warning)
	}
	if err := gate.Check(); err != nil {
		return nil, err
	}

	variables, err := resolveVariables(ctx, gate, cfg)
	if err != nil {
		return nil, err
	}

	plans := appPlans(cfg, variables)
	clients := clientApps(plans)
	if prebuilt {
		if err := clientenv.CheckFresh(cfg.Dir, clients); err != nil {
			return nil, err
		}
		ui.Diagnostic("using prebuilt output in .ocel/output")
	} else {
		if err := clientenv.Generate(clients); err != nil {
			return nil, err
		}
		if err := d.buildApp(ctx, cfg, buildEnv(plans), buildOut); err != nil {
			return nil, err
		}
		if err := clientenv.Record(cfg.Dir, clients); err != nil {
			return nil, err
		}
	}

	functions, err := d.collectAppFunctions(cfg.Dir)
	if err != nil {
		return nil, err
	}

	if len(functions) == 0 {
		if len(resources) == 0 {
			return nil, nil
		}
		ui.Diagnostic("no functions to deploy; deploying infrastructure only")
	}

	return manifestbuilder.Build(cfg.Slug, cfg.Domains, toApps(cfg.Apps, usages), toDeclarations(cfg.Dir, resources), functions, variablesByApp(variables, functions))
}

const maxCapturedDiscoveryOutput = 4096

type boundedCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *boundedCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if room := maxCapturedDiscoveryOutput - c.buf.Len(); room > 0 {
		if room > len(p) {
			room = len(p)
		}
		c.buf.Write(p[:room])
	}
	return len(p), nil
}

func (c *boundedCapture) annotate(err error) error {
	c.mu.Lock()
	text := strings.TrimSpace(c.buf.String())
	c.mu.Unlock()
	if text == "" {
		return err
	}
	return fmt.Errorf("%w\n%s", err, text)
}

const rootApp = "this project's app"

func resolveVariables(ctx context.Context, gate *envgate.Gate, cfg *projectconfig.Config) (map[string][]manifestbuilder.Variable, error) {
	definitions := gate.Definitions()
	variables := make(map[string][]manifestbuilder.Variable, len(cfg.Apps))
	for _, app := range envApps(cfg) {
		resolved, err := gate.Resolve(ctx, app.Name)
		if err != nil {
			return nil, err
		}
		variables[app.Name] = appVariables(definitions, resolved)
	}
	return variables, nil
}

func appVariables(definitions []*resourcesv1.VariableDefinition, resolved map[string]envgate.Resolved) []manifestbuilder.Variable {
	variables := make([]manifestbuilder.Variable, 0, len(definitions))
	for _, definition := range definitions {
		cell, ok := resolved[definition.GetKey()]
		if !ok {
			continue
		}
		variables = append(variables, manifestbuilder.Variable{
			Key:              definition.GetKey(),
			Class:            definition.GetClass(),
			Value:            cell.Value,
			Folder:           cell.Folder,
			Version:          cell.Version,
			ClientAccessible: definition.GetClientAccessible(),
		})
	}
	return variables
}

func variablesByApp(variables map[string][]manifestbuilder.Variable, functions []manifestbuilder.Function) map[string][]manifestbuilder.Variable {
	root, ok := variables[rootApp]
	if !ok {
		return variables
	}
	byApp := make(map[string][]manifestbuilder.Variable, len(functions))
	for _, f := range functions {
		byApp[f.App] = root
	}
	return byApp
}

type appPlan struct {
	name      string
	dir       string
	variables []manifestbuilder.Variable
}

func appPlans(cfg *projectconfig.Config, variables map[string][]manifestbuilder.Variable) []appPlan {
	if len(cfg.Apps) == 0 {
		return []appPlan{{dir: cfg.Dir, variables: variables[rootApp]}}
	}
	plans := make([]appPlan, 0, len(cfg.Apps))
	for _, a := range cfg.Apps {
		plans = append(plans, appPlan{
			name:      a.Name,
			dir:       filepath.Join(cfg.Dir, a.Path),
			variables: variables[a.Name],
		})
	}
	return plans
}

func buildEnv(plans []appPlan) map[string]map[string]string {
	byApp := make(map[string]map[string]string, len(plans))
	for _, plan := range plans {
		env := make(map[string]string)
		for _, v := range plan.variables {
			if v.Class != resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN {
				continue
			}
			env[v.Key] = v.Value
		}
		byApp[plan.name] = env
	}
	return byApp
}

func clientApps(plans []appPlan) []clientenv.App {
	apps := make([]clientenv.App, 0, len(plans))
	for _, plan := range plans {
		apps = append(apps, clientenv.App{Name: plan.name, Dir: plan.dir, Variables: plan.variables})
	}
	return apps
}

func envScope(cfg *projectconfig.Config, preview bool, environment string) envgate.Scope {
	return envgate.Scope{Apps: envApps(cfg), Preview: preview, Environment: environment}
}

func envApps(cfg *projectconfig.Config) []envgate.App {
	if len(cfg.Apps) == 0 {
		return []envgate.App{{Name: rootApp}}
	}
	apps := make([]envgate.App, 0, len(cfg.Apps))
	for _, a := range cfg.Apps {
		apps = append(apps, envgate.App{Name: a.Name, Folder: a.Folder})
	}
	return apps
}

type runnerValues struct {
	runner  *providerrunner.Runner
	options []byte
	slug    string
	class   deploymentsv1.Environment_Class
}

func (v runnerValues) List(ctx context.Context) ([]envgate.Stored, error) {
	vars, err := v.runner.Vars()
	if err != nil {
		return nil, err
	}
	resp, err := vars.ListValues(ctx, &envv1.ListValuesRequest{
		Options:         v.options,
		ProtocolVersion: manifestbuilder.SchemaVersion,
		Class:           v.class,
		Slug:            v.slug,
	})
	if err != nil {
		return nil, err
	}

	var stored []envgate.Stored
	for _, value := range resp.GetValues() {
		c := value.GetCoordinate()
		stored = append(stored, envgate.Stored{
			Address: envgate.Address{
				Cell:        envgate.Cell{Key: c.GetKey(), Folder: c.GetFolder()},
				Environment: c.GetEnvironment(),
			},
			Version: value.GetVersion(),
		})
	}
	return stored, nil
}

func (v runnerValues) Reveal(ctx context.Context, rows []envgate.Address) (map[envgate.Cell]string, error) {
	named := make([]*envv1.Cell, 0, len(rows))
	for _, row := range rows {
		named = append(named, &envv1.Cell{Folder: row.Cell.Folder, Key: row.Cell.Key, Environment: row.Environment})
	}
	vars, err := v.runner.Vars()
	if err != nil {
		return nil, err
	}
	resp, err := vars.RevealValues(ctx, &envv1.RevealValuesRequest{
		Options:         v.options,
		ProtocolVersion: manifestbuilder.SchemaVersion,
		Class:           v.class,
		Slug:            v.slug,
		Cells:           named,
	})
	if err != nil {
		return nil, errors.New(err.Error())
	}

	found := make(map[envgate.Cell]string, len(resp.GetValues()))
	for _, value := range resp.GetValues() {
		c := value.GetMetadata().GetCoordinate()
		found[envgate.Cell{Key: c.GetKey(), Folder: c.GetFolder()}] = value.GetValue()
	}
	return found, nil
}

func toApps(apps []projectconfig.App, usages []attribution.Usage) []manifestbuilder.App {
	byApp := make(map[string][]manifestbuilder.Usage, len(apps))
	for _, u := range usages {
		byApp[u.App] = append(byApp[u.App], manifestbuilder.Usage{Type: u.Type, ID: u.ID, Files: u.Files})
	}

	out := make([]manifestbuilder.App, 0, len(apps))
	for _, a := range apps {
		out = append(out, manifestbuilder.App{
			Name:      a.Name,
			Framework: a.Framework,
			Domains:   a.Domains,
			Folder:    a.Folder,
			Usages:    byApp[a.Name],
		})
	}
	return out
}

func toAttributionApps(apps []projectconfig.App) []attribution.App {
	out := make([]attribution.App, 0, len(apps))
	for _, a := range apps {
		out = append(out, attribution.App{Name: a.Name, Path: a.Path})
	}
	return out
}

func failSession(ctx context.Context, ui *deployui.Session, err error) error {
	if ctx.Err() != nil {
		ui.Cancel()
		return &ExitError{Code: interruptExitCode}
	}
	ui.Fail(err)
	return &ExitError{Code: 1}
}

func runProviderSession(ctx context.Context, d deps, cfg *projectconfig.Config, provider *projectconfig.ProviderDescriptor, stdout, stderr io.Writer, drive func(*providerrunner.Runner) error) error {
	binPath, err := d.locateProviderBinary(ctx, cfg.Dir, provider.Package)
	if err != nil {
		return fmt.Errorf("locate provider binary: %w", err)
	}

	env, err := workerBundleEnv(cfg.Dir)
	if err != nil {
		return err
	}

	runner, err := providerrunner.Spawn(ctx, providerrunner.Config{
		BinaryPath:   binPath,
		Stdout:       stdout,
		Stderr:       stderr,
		Env:          env,
		ReadyTimeout: deployReadyTimeout,
	})
	if err != nil {
		return fmt.Errorf("spawn provider: %w", err)
	}
	defer runner.Close()

	if err := runner.Ready(ctx); err != nil {
		return err
	}
	return drive(runner)
}

func workerBundleEnv(projectDir string) ([]string, error) {
	bundles, err := json.Marshal(node.WorkerBundles(projectDir))
	if err != nil {
		return nil, fmt.Errorf("marshal worker bundles: %w", err)
	}
	store, err := json.Marshal(node.StoreWorkerBundles(projectDir))
	if err != nil {
		return nil, fmt.Errorf("marshal store worker bundles: %w", err)
	}
	isrWriter, err := json.Marshal(node.ISRWriterBundles(projectDir))
	if err != nil {
		return nil, fmt.Errorf("marshal isr writer worker bundles: %w", err)
	}
	return append(os.Environ(),
		"OCEL_WORKER_BUNDLES="+string(bundles),
		"OCEL_STORE_WORKER_BUNDLES="+string(store),
		"OCEL_ISR_WRITER_WORKER_BUNDLES="+string(isrWriter),
	), nil
}

func confirmDeploy(ctx context.Context, slug, providerPackage string, knownSlugs []string, stdout io.Writer, stdin io.Reader) (bool, error) {
	if len(knownSlugs) == 0 {
		return confirmYN(ctx, fmt.Sprintf("Deploy %s with %s?", slug, providerPackage), stdout, stdin)
	}
	fmt.Fprintf(stdout, "No existing deployment for slug %q.\nThis will create a NEW project.\nThis backend already has: %s\n",
		slug, strings.Join(knownSlugs, ", "))
	return confirmYN(ctx, "Continue?", stdout, stdin)
}

func toDeclarations(configDir string, resources []declare.Resource) []manifestbuilder.Declaration {
	decls := make([]manifestbuilder.Declaration, len(resources))
	for i, r := range resources {
		var source string
		if frame, ok := attribution.DeclaringFrame(configDir, r.Stack); ok {
			source = frame.String()
		}
		decls[i] = manifestbuilder.Declaration{
			Type:     r.Type,
			ID:       r.Name,
			Postgres: r.Postgres,
			Bucket:   r.Bucket,
			Source:   source,
		}
	}
	return decls
}

func toAttributionDeclarations(resources []declare.Resource) []attribution.Declaration {
	decls := make([]attribution.Declaration, len(resources))
	for i, r := range resources {
		decls[i] = attribution.Declaration{Type: r.Type, ID: r.Name, Stack: r.Stack}
	}
	return decls
}
