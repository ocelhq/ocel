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

	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/deploycollector"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerlocator"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	providerv1 "github.com/ocelhq/ocel/pkg/proto/provider/v1"
)

// deployReadyTimeout overrides how long `ocel deploy` waits for the spawned
// provider to signal readiness (see providerrunner.Config.ReadyTimeout);
// zero defers to providerrunner's own default/env resolution. A var so
// tests can shorten it, mirroring watchDebounce in dev.go.
var deployReadyTimeout time.Duration

// locateProviderBinary is a seam over providerlocator.Locate so tests can
// point `ocel deploy` at a fake provider binary without a real npm install.
var locateProviderBinary = providerlocator.Locate

// deployOptions holds the flags accepted by `ocel deploy`.
type deployOptions struct {
	yes bool
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
}

// runDeploy resolves the project config, verifies auth, requires a
// configured provider, confirms with the user, discovers and collects
// declared resources, builds the deploy manifest, locates and spawns the
// provider binary, drives its Deploy RPC to a terminal result, and tears
// the provider down. Every pre-spawn error — not logged in, no provider
// configured, malformed or missing config — is returned before anything is
// spawned.
//
// Deploy makes no call to the Ocel API this slice: the project id comes
// from the resolved config, and the manifest is built entirely locally
// (see the ocelhq-x53 PRD's "Login gate only" decision).
func runDeploy(ctx context.Context, cwd string, opts deployOptions, stdout, stderr io.Writer, stdin io.Reader) error {
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

	resources, err := deploycollector.Collect(ctx, cfg, stdout, stderr)
	if err != nil {
		return err
	}

	manifest, err := manifestbuilder.Build(cfg.ProjectID, toDeclarations(resources))
	if err != nil {
		return err
	}

	return runProviderSession(ctx, cfg, provider, stdout, stderr, func(runner *providerrunner.Runner) error {
		req := &providerv1.DeployRequest{
			Manifest:        manifest,
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
		}

		// The provider reports the whole stack's connection outputs on the
		// terminal ResultEvent. We collect them here; the CLI does not consume
		// them yet (see the ocelhq-c4d spec's outputs decision).
		var stackOutputs []*providerv1.ResourceOutput
		onEvent := func(ev *providerv1.DeployEvent) {
			streamDeployEvent(stdout, ev)
			if res := ev.GetResult(); res != nil {
				stackOutputs = res.GetOutputs()
			}
		}
		if err := runner.Deploy(ctx, req, onEvent); err != nil {
			return err
		}
		_ = stackOutputs

		fmt.Fprintln(stdout, "✓ Deploy succeeded.")
		return nil
	})
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
		}
	}
	return decls
}

// streamDeployEvent prints a DeployEvent's progress/log message to stdout.
// The terminal ResultEvent needs no extra printing here: runner.Deploy
// already turns a failure result into a *providerrunner.DeployFailedError
// and a success result into a nil return.
func streamDeployEvent(stdout io.Writer, ev *providerv1.DeployEvent) {
	if p := ev.GetProgress(); p != nil {
		fmt.Fprintln(stdout, p.GetMessage())
		return
	}
	if l := ev.GetLog(); l != nil {
		fmt.Fprintln(stdout, l.GetMessage())
	}
}
