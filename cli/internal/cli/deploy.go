package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/appbuilder"
	"github.com/ocelhq/ocel/cli/internal/clientenv"
	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/deploycollector"
	"github.com/ocelhq/ocel/cli/internal/deployresult"
	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerlocator"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	"github.com/ocelhq/ocel/cli/platform"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	envv1 "github.com/ocelhq/ocel/pkg/proto/env/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

// deployReadyTimeout overrides how long `ocel deploy` waits for the spawned
// provider to signal readiness (see providerrunner.Config.ReadyTimeout);
// zero defers to providerrunner's own default/env resolution. A var so
// tests can shorten it, mirroring watchDebounce in dev.go.
var deployReadyTimeout time.Duration

// locateProviderBinary is a seam over providerlocator.Locate so tests can
// point `ocel deploy` at a fake provider binary without a real npm install.
var locateProviderBinary = providerlocator.Locate

// buildApp and collectAppFunctions are seams over appbuilder so tests can drive
// the build and its output separately — asserting that `--prebuilt` skips the
// former — without spawning the embedded node-builder, mirroring
// locateProviderBinary.
var (
	buildApp            = appbuilder.Build
	collectAppFunctions = appbuilder.CollectFunctions
)

// stdinIsTerminal is a seam over isReaderTTY for the one decision no test can
// otherwise reach: whether a refused deploy may stop and hand the developer the
// variables UI. Every test drives these commands with an in-memory reader, so
// without the seam the waiting path is unreachable and the rule that a
// non-interactive run hard-fails is unfalsifiable.
var stdinIsTerminal = func(r io.Reader) bool { return isReaderTTY(r) }

// noBrowserEnvVar opts a run out of the browser handoff without a flag: a
// developer at a terminal over SSH is interactive by every signal the CLI has
// and still has no browser to be handed.
const noBrowserEnvVar = "OCEL_NO_BROWSER"

// canOpenVarsUI reports whether a gate refusal may become a wait on the
// variables UI rather than the end of the run. There is no CI detection here
// on purpose: a terminal on stdin is the signal, and anything it gets wrong is
// answered by --no-ui or OCEL_NO_BROWSER rather than by guessing at an
// environment.
func canOpenVarsUI(stdin io.Reader, noUI bool) bool {
	if noUI || os.Getenv(noBrowserEnvVar) != "" {
		return false
	}
	return stdinIsTerminal(stdin)
}

// noUIFlagUsage documents --no-ui everywhere it is registered.
const noUIFlagUsage = "Never pause to open the variables UI; fail on a missing or invalid variable instead"

// deployOptions holds the flags accepted by `ocel deploy`.
type deployOptions struct {
	yes      bool
	tag      string
	prebuilt bool
	noUI     bool
}

var deployOpts deployOptions

// deployCmd deploys the current Ocel project to its configured provider.
var deployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "Deploy your project to its configured cloud provider",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}

		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		return runDeploy(ctx, cwd, deployOpts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	},
}

func init() {
	deployCmd.Flags().BoolVarP(&deployOpts.yes, "yes", "y", false, "Skip the confirmation prompt")
	deployCmd.Flags().StringVar(&deployOpts.tag, "tag", "", "Stamp this deploy with an immutable label to roll back to later (`ocel rollback --tag <tag>`)")
	deployCmd.Flags().BoolVar(&deployOpts.prebuilt, "prebuilt", false, prebuiltFlagUsage)
	deployCmd.Flags().BoolVar(&deployOpts.noUI, "no-ui", false, noUIFlagUsage)
}

// prebuiltFlagUsage documents --prebuilt everywhere it is registered. The flag
// is an assertion by the caller that `ocel build` has already run against this
// checkout; nothing verifies the output is current.
const prebuiltFlagUsage = "Deploy the existing .ocel/output instead of building the apps first (produce it with ocel build)"

// runDeploy resolves the project config, requires a configured provider,
// spawns the provider binary, and preflights it — authenticating the
// credentials the deploy needs and printing the "Running with:" banner — before
// the user confirms and before the app build runs, so a missing or invalid
// credential aborts up front rather than after paying for a build. It then
// builds the deploy manifest, drives the provider's Deploy RPC to a terminal
// result, and tears the provider down. Every pre-spawn error — no provider
// configured, malformed or missing config — is returned before anything is
// spawned.
//
// Deploy makes no call to the Ocel API: the slug comes from the resolved
// config, and the manifest is built entirely locally. It authenticates only to
// the user's own cloud account, through the provider binary.
func runDeploy(ctx context.Context, cwd string, opts deployOptions, stdout, stderr io.Writer, stdin io.Reader) error {
	if err := validateTag(opts.tag); err != nil {
		return err
	}

	cfg, err := projectconfig.Resolve(cwd)
	if err != nil {
		return err
	}

	if err := platform.Ensure(cfg.Dir); err != nil {
		return err
	}

	provider, err := cfg.RequireProvider()
	if err != nil {
		return err
	}

	if err := deployresult.Clear(cfg.Dir); err != nil {
		return err
	}

	ui := deployui.New(stdout, cfg.Dir, "ocel deploy", verboseEnabled())
	defer ui.Close()

	provW := ui.BuildWriter()
	err = runProviderSession(ctx, cfg, provider, provW, provW, func(runner *providerrunner.Runner) error {
		knownSlugs, err := preflightDeploy(ctx, runner, provider, cfg.Slug, stdout)
		if err != nil {
			return err
		}

		if !opts.yes && isReaderTTY(stdin) {
			proceed, err := confirmDeploy(cfg.Slug, provider.Package, knownSlugs, stdout, stdin)
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
			cfg:      cfg,
			provider: provider,
			runner:   runner,
			newGate: func() *envgate.Gate {
				return envgate.New(runnerValues{
					runner:  runner,
					options: []byte(provider.Options),
					slug:    cfg.Slug,
					class:   deploymentsv1.Environment_CLASS_PRODUCTION,
				}, envScope(cfg, false))
			},
			ui:      ui,
			stdout:  stdout,
			enabled: canOpenVarsUI(stdin, opts.noUI),
		}
		manifest, err := recovery.buildManifest(ctx, opts.prebuilt, ui.BuildWriter())
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

		var stackOutputs []*deploymentsv1.ResourceOutput
		var appURLs []string
		var promotionID string
		onEvent := func(ev *deploymentsv1.DeployEvent) {
			ui.Event(ev)
			if res := ev.GetResult(); res != nil {
				stackOutputs = res.GetOutputs()
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
		ui.Deployed("Deployed", appURLs, stackOutputs)
		return nil
	})
	if err != nil {
		return failSession(ctx, ui, err)
	}
	return nil
}

// collectAndBuildManifest runs the pre-provision path `ocel deploy` and `ocel
// preview` share: it collects the declared infrastructure, builds the
// project's apps into functions (discovered from the build output), and lowers
// both into the provider Manifest. When the build yields no functions it warns
// and proceeds infrastructure-only; when there is nothing at all — no functions
// and no resources — it returns a nil manifest so the caller can exit cleanly.
// Any app-build failure aborts here, before any provider is spawned.
//
// prebuilt skips the framework build and deploys whatever is already in
// .ocel/output; the infrastructure discovery pass, and the variable gate that
// follows it, run either way.
func collectAndBuildManifest(ctx context.Context, cfg *projectconfig.Config, gate *envgate.Gate, prebuilt bool, buildOut io.Writer) (*deploymentsv1.Manifest, error) {
	resources, err := deploycollector.Collect(ctx, cfg, gate, buildOut, buildOut)
	if err != nil {
		return nil, err
	}

	// Both checks stand between discovery and the build on purpose: a deploy
	// that cannot succeed must not cost one.
	warnings, err := envgate.Lint(gate.Definitions(), envApps(cfg), filepath.Join(cfg.Dir, projectconfig.ConfigFileName))
	if err != nil {
		return nil, err
	}
	for _, warning := range warnings {
		fmt.Fprintln(buildOut, "warning: "+warning)
	}
	if err := gate.Check(); err != nil {
		return nil, err
	}

	variables, err := resolveVariables(ctx, gate, cfg)
	if err != nil {
		return nil, err
	}

	if prebuilt {
		fmt.Fprintln(buildOut, "using prebuilt output in .ocel/output")
	} else {
		if err := buildApp(ctx, cfg, buildEnv(variables), buildOut); err != nil {
			return nil, err
		}
	}

	functions, err := collectAppFunctions(cfg.Dir)
	if err != nil {
		return nil, err
	}

	if len(functions) == 0 {
		if len(resources) == 0 {
			return nil, nil
		}
		fmt.Fprintln(buildOut, "no functions to deploy; deploying infrastructure only")
	}

	return manifestbuilder.Build(cfg.Slug, cfg.Domains, toApps(cfg.Apps), toDeclarations(resources), functions, variablesByApp(variables, functions))
}

// rootApp stands in for the app a project that configures none still deploys:
// the one the builder detects at the project root, whose name nothing knows
// until the build has run. It binds no folder, so it resolves exactly what the
// project root holds, and variablesByApp re-keys that resolution onto the
// detected app once the build has named it.
const rootApp = "this project's app"

// resolveVariables is what each app is deployed with: the gate's resolution
// paired with the class each key was declared under. The class decides
// delivery and the resolution decides the value, so neither half alone is
// deployable.
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

// appVariables joins one app's resolution to the declarations. A key the app
// resolves no cell for is absent rather than empty: the SDK's named error is
// the whole remedy for reading one, and a blank value would defeat it.
//
// The folder travels alongside the value because resolution is the only place
// that knows it. A live-class cell carries no plaintext at all, so its folder
// is what makes it addressable later; dropping it would leave the manifest
// naming a key nothing can be asked for.
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
			ClientAccessible: definition.GetClientAccessible(),
		})
	}
	return variables
}

// variablesByApp keys a resolution by the app name the manifest will carry.
// Only the root stand-in needs re-keying, and it applies to every app the
// build produced, because a project that configures none deploys exactly one.
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

// buildEnv is what each app's build runs with: its own plaintext values, keyed
// by the app the builder will run them for. Only the plaintext class belongs
// in a build process's environment at all — nothing else is a build's to read.
//
// The root stand-in is keyed by no app, because nothing yet knows the name of
// the app the builder will detect for a project that configures none.
func buildEnv(variables map[string][]manifestbuilder.Variable) map[string]map[string]string {
	byApp := make(map[string]map[string]string, len(variables))
	for app, vars := range variables {
		env := make(map[string]string)
		for _, v := range vars {
			if v.Class != resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN {
				continue
			}
			env[v.Key] = v.Value
			// A client-accessible value is exported a second time, under the
			// framework's public prefix: that name, and only that name, is what
			// the framework's static replacement inlines into a browser bundle.
			if v.ClientAccessible {
				env[clientenv.PublicName(v.Key)] = v.Value
			}
		}
		if app == rootApp {
			app = ""
		}
		byApp[app] = env
	}
	return byApp
}

// envScope is what a gate refusal has to name to be actionable: the apps that
// read a cell, with the folders they bind, and the substrate the fixing
// command must address.
func envScope(cfg *projectconfig.Config, preview bool) envgate.Scope {
	return envgate.Scope{Apps: envApps(cfg), Preview: preview}
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

// runnerValues reaches the variable store the only way the CLI can: through
// the provider binary it already has a session with.
type runnerValues struct {
	runner  *providerrunner.Runner
	options []byte
	slug    string
	class   deploymentsv1.Environment_Class
}

func (v runnerValues) List(ctx context.Context) ([]envgate.Stored, error) {
	resp, err := v.runner.ListValues(ctx, &envv1.ListValuesRequest{
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
			Cell:        envgate.Cell{Key: c.GetKey(), Folder: c.GetFolder()},
			Environment: c.GetEnvironment(),
			Version:     value.GetVersion(),
		})
	}
	return stored, nil
}

func (v runnerValues) Reveal(ctx context.Context, cells []envgate.Cell) (map[envgate.Cell]string, error) {
	named := make([]*envv1.Cell, 0, len(cells))
	for _, cell := range cells {
		named = append(named, &envv1.Cell{Folder: cell.Folder, Key: cell.Key})
	}
	resp, err := v.runner.RevealValues(ctx, &envv1.RevealValuesRequest{
		Options:         v.options,
		ProtocolVersion: manifestbuilder.SchemaVersion,
		Class:           v.class,
		Slug:            v.slug,
		Cells:           named,
	})
	if err != nil {
		// Flattened to its message: this error travels back out through the
		// gate's own handler, which adopts a connect error it can see whole and
		// would answer with the transport's word in place of the cells the gate
		// named.
		return nil, errors.New(err.Error())
	}

	found := make(map[envgate.Cell]string, len(resp.GetValues()))
	for _, value := range resp.GetValues() {
		c := value.GetMetadata().GetCoordinate()
		found[envgate.Cell{Key: c.GetKey(), Folder: c.GetFolder()}] = value.GetValue()
	}
	return found, nil
}

// toApps lowers the resolved config's apps into the manifest builder's input.
// A project that configures none still yields an app: the builder detects one
// and the manifest builder recovers it from the functions it emitted.
func toApps(apps []projectconfig.App) []manifestbuilder.App {
	out := make([]manifestbuilder.App, 0, len(apps))
	for _, a := range apps {
		out = append(out, manifestbuilder.App{
			Name:      a.Name,
			Framework: a.Framework,
			Domains:   a.Domains,
			Folder:    a.Folder,
		})
	}
	return out
}

// failSession ends a deploy/preview/bootstrap run on error: it renders a
// cancellation when the context was interrupted, otherwise a failure, and
// returns the sentinel exit error. It centralises the terminal error handling
// the provider-driving commands share.
func failSession(ctx context.Context, ui *deployui.Session, err error) error {
	if ctx.Err() != nil {
		ui.Cancel()
	} else {
		ui.Fail(err)
	}
	return &ExitError{Code: 1}
}

// runProviderSession locates and spawns the project's configured provider,
// waits for it to signal readiness, hands the ready runner to drive, and
// tears the provider down afterward. It centralises the spawn/ready/teardown
// plumbing that `ocel deploy` and `ocel bootstrap` share; only the RPC each
// drives differs.
func runProviderSession(ctx context.Context, cfg *projectconfig.Config, provider *projectconfig.ProviderDescriptor, stdout, stderr io.Writer, drive func(*providerrunner.Runner) error) error {
	binPath, err := locateProviderBinary(cfg.Dir, provider.Package)
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

// workerBundleEnv is the provider's environment: the inherited one plus the
// two manifests naming the edge worker bundles in the project's materialized
// platform dist. The provider binary is a separate process in a separate Go
// module, so env is how it learns those paths (see cloud/edge/bundles.go).
func workerBundleEnv(projectDir string) ([]string, error) {
	bundles, err := json.Marshal(platform.WorkerBundles(projectDir))
	if err != nil {
		return nil, fmt.Errorf("marshal worker bundles: %w", err)
	}
	store, err := json.Marshal(platform.StoreWorkerBundles(projectDir))
	if err != nil {
		return nil, fmt.Errorf("marshal store worker bundles: %w", err)
	}
	return append(os.Environ(),
		"OCEL_WORKER_BUNDLES="+string(bundles),
		"OCEL_STORE_WORKER_BUNDLES="+string(store),
	), nil
}

// confirmDeploy prints the "Deploy <project> with <provider>? [y/N]" prompt
// and returns the user's yes/no answer (see confirmYN).
//
// knownSlugs is the slug-drift guard: the slug is a project's only thread back
// to its infrastructure, so deploying a mistyped or renamed one orphans
// production and stands a parallel copy of everything up beside it, and the
// routine prompt looks identical either way. When the provider reports other
// projects in its backend — which it does only when this slug has nothing there
// — the prompt says so instead. An empty backend reports nothing, so a genuine
// first deploy is never nagged. It stays a y/N, not a refusal: forking a new
// project is legitimate, it just has to be deliberate.
func confirmDeploy(slug, providerPackage string, knownSlugs []string, stdout io.Writer, stdin io.Reader) (bool, error) {
	if len(knownSlugs) == 0 {
		return confirmYN(fmt.Sprintf("Deploy %s with %s?", slug, providerPackage), stdout, stdin)
	}
	fmt.Fprintf(stdout, "No existing deployment for slug %q.\nThis will create a NEW project.\nThis backend already has: %s\n",
		slug, strings.Join(knownSlugs, ", "))
	return confirmYN("Continue?", stdout, stdin)
}

// confirmYN prints "<prompt> [y/N] " and reads a single line from stdin,
// defaulting to No on anything but an explicit y/yes answer — including no
// answer at all (e.g. a closed stdin), so an interrupted or empty read never
// accidentally proceeds.
func confirmYN(prompt string, stdout io.Writer, stdin io.Reader) (bool, error) {
	fmt.Fprintf(stdout, "%s [y/N] ", prompt)

	scanner := bufio.NewScanner(stdin)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return false, fmt.Errorf("failed to read input: %w", err)
		}
		return false, nil
	}

	answer := strings.TrimSpace(scanner.Text())
	return strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes"), nil
}

// toDeclarations adapts the deploy collector's full Declare records
// (cli/internal/declare.Resource) into the manifest builder's pure input
// shape (manifestbuilder.Declaration). Source is left empty: the collector
// doesn't yet capture a declaration's source location, so duplicate errors
// fall back to manifestbuilder's "<unknown source>".
func toDeclarations(resources []declare.Resource) []manifestbuilder.Declaration {
	decls := make([]manifestbuilder.Declaration, len(resources))
	for i, r := range resources {
		decls[i] = manifestbuilder.Declaration{
			Type:     r.Type,
			ID:       r.Name,
			Postgres: r.Postgres,
			Bucket:   r.Bucket,
		}
	}
	return decls
}
