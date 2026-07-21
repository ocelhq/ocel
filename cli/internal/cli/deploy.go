package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/appbuilder"
	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/deploycollector"
	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerlocator"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// deployReadyTimeout overrides how long `ocel deploy` waits for the spawned
// provider to signal readiness (see providerrunner.Config.ReadyTimeout);
// zero defers to providerrunner's own default/env resolution. A var so
// tests can shorten it, mirroring watchDebounce in dev.go.
var deployReadyTimeout time.Duration

// locateProviderBinary is a seam over providerlocator.Locate so tests can
// point `ocel deploy` at a fake provider binary without a real npm install.
var locateProviderBinary = providerlocator.Locate

// buildAppFunctions is a seam over appbuilder.Build so tests can inject canned
// functions without spawning the embedded node-builder, mirroring
// locateProviderBinary.
var buildAppFunctions = appbuilder.Build

// deployOptions holds the flags accepted by `ocel deploy`.
type deployOptions struct {
	yes bool
	tag string
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
}

// runDeploy resolves the project config, verifies auth, requires a
// configured provider, spawns the provider binary, and preflights it —
// authenticating the credentials the deploy needs and printing the "Running
// with:" banner — before the user confirms and before the app build runs, so a
// missing or invalid credential aborts up front rather than after paying for a
// build. It then builds the deploy manifest, drives the provider's Deploy RPC
// to a terminal result, and tears the provider down. Every pre-spawn error —
// not logged in, no provider configured, malformed or missing config — is
// returned before anything is spawned.
//
// Deploy makes no call to the Ocel API: the project id comes from the
// resolved config, and the manifest is built entirely locally. Login is only
// gated to confirm the user is authenticated.
func runDeploy(ctx context.Context, cwd string, opts deployOptions, stdout, stderr io.Writer, stdin io.Reader) error {
	if err := validateTag(opts.tag); err != nil {
		return err
	}

	if _, err := loadCredentials(); err != nil {
		fmt.Fprintln(stderr, "You're not logged in. Run `ocel login` first.")
		return &ExitError{Code: 1}
	}

	cfg, err := projectconfig.Resolve(cwd)
	if err != nil {
		return err
	}

	provider, err := cfg.RequireProvider()
	if err != nil {
		return err
	}

	ui := deployui.New(stdout, cfg.Dir, "ocel deploy", verboseEnabled())
	defer ui.Close()

	provW := ui.BuildWriter()
	err = runProviderSession(ctx, cfg, provider, provW, provW, func(runner *providerrunner.Runner) error {
		if err := preflightClass(ctx, runner, provider, deploymentsv1.Environment_CLASS_PRODUCTION, "ocel bootstrap", stdout); err != nil {
			return err
		}

		if !opts.yes && isReaderTTY(stdin) {
			proceed, err := confirmDeploy(cfg.ProjectID, provider.Package, stdout, stdin)
			if err != nil {
				return err
			}
			if !proceed {
				fmt.Fprintln(stdout, "Aborted.")
				return nil
			}
		}

		ui.Building()
		manifest, err := collectAndBuildManifest(ctx, cfg, ui.BuildWriter())
		if err != nil {
			return err
		}
		if manifest == nil {
			ui.Finish("Nothing to deploy")
			return nil
		}
		ui.BuildOK()

		req := &deploymentsv1.DeployRequest{
			Manifest:        manifest,
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			Environment: &deploymentsv1.Environment{
				Class:     deploymentsv1.Environment_CLASS_PRODUCTION,
				Lifecycle: deploymentsv1.Environment_LIFECYCLE_UNSPECIFIED,
			},
			Tag: opts.tag,
		}

		var stackOutputs []*deploymentsv1.ResourceOutput
		var appURLs []string
		onEvent := func(ev *deploymentsv1.DeployEvent) {
			ui.Event(ev)
			if res := ev.GetResult(); res != nil {
				stackOutputs = res.GetOutputs()
				appURLs = res.GetAppUrls()
			}
		}
		if err := runner.Deploy(ctx, req, onEvent); err != nil {
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
func collectAndBuildManifest(ctx context.Context, cfg *projectconfig.Config, buildOut io.Writer) (*deploymentsv1.Manifest, error) {
	resources, err := deploycollector.Collect(ctx, cfg, buildOut, buildOut)
	if err != nil {
		return nil, err
	}

	functions, err := buildAppFunctions(ctx, cfg, buildOut)
	if err != nil {
		return nil, err
	}

	if len(functions) == 0 {
		if len(resources) == 0 {
			return nil, nil
		}
		fmt.Fprintln(buildOut, "no functions to deploy; deploying infrastructure only")
	}

	return manifestbuilder.Build(cfg.ProjectID, cfg.Domains, toApps(cfg.Apps), toDeclarations(resources), functions)
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

	runner, err := providerrunner.Spawn(ctx, providerrunner.Config{
		BinaryPath:   binPath,
		Stdout:       stdout,
		Stderr:       stderr,
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

// confirmDeploy prints the "Deploy <project> with <provider>? [y/N]" prompt
// and returns the user's yes/no answer (see confirmYN).
func confirmDeploy(projectID, providerPackage string, stdout io.Writer, stdin io.Reader) (bool, error) {
	return confirmYN(fmt.Sprintf("Deploy %s with %s?", projectID, providerPackage), stdout, stdin)
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
