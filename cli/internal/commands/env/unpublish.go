package env

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/pkg/progress"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
)

func newUnpublishCommand(dependencies Dependencies) *cobra.Command {
	var opts envOptions
	var publisher string
	cmd := &cobra.Command{
		Use:   "unpublish",
		Short: "Remove the values one infrastructure program wrote, at the versions it wrote, read as JSON on stdin",
		Long: "Remove the values one infrastructure program wrote, at the versions it wrote, read as JSON on stdin.\n\n" +
			"The input is a provider.variablestore.v1.PublishedVersions in protobuf JSON, each key with the version " +
			"`ocel env publish --json` reported writing:\n\n" +
			"  {\"values\": [{\"key\": \"API_KEY\", \"version\": \"3\"}]}\n\n" +
			"A value the publisher has written again since is left in place, so a program that moved the call " +
			"publishing it keeps the values the new call wrote when the old one is removed.",
		Example: "  $ ocel env unpublish --publisher urn:pulumi:prod::infra < versions.json",
		Args:    cobra.NoArgs,
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return withCommand(cmd, dependencies, func(ctx context.Context, cwd string) error {
			return runEnvUnpublish(ctx, dependencies, cwd, publisher, cmd.InOrStdin(), opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	}
	previewFlag(cmd, &opts)
	environmentFlag(cmd, &opts)
	cmd.Flags().StringVar(&publisher, "publisher", "", "The stable `id` of the program that published these values")
	_ = cmd.MarkFlagRequired("publisher")
	return commands.DeclareResult(commands.DeclareMutating(cmd), &resultv1.EnvUnpublishResult{})
}

func runEnvUnpublish(ctx context.Context, dependencies Dependencies, cwd, publisher string, stdin io.Reader, opts envOptions, stdout, stderr io.Writer) error {
	written, err := decodePublishedVersions(stdin)
	if err != nil {
		return err
	}
	return withEnvProvider(ctx, dependencies, cwd, opts, "ocel env unpublish", []cloudv1.Scope{cloudv1.Scope_SCOPE_VARIABLES_WRITE}, stderr, func(ctx context.Context, run *run.Run, provider *providerprocess.Provider, cfg *project.Project, _ *contractv1.PreflightResponse) error {
		variableStore, err := provider.VariableStore()
		if err != nil {
			return err
		}
		var resp *variablestorev1.UnpublishValuesResponse
		err = runInSpan(run, progressv1.Phase_PHASE_DESTROY, cfg, progress.Removing.Title(describeTierValues(opts)+" "+publisher+" published"), func() (err error) {
			resp, err = variableStore.UnpublishValues(ctx, &variablestorev1.UnpublishValuesRequest{
				Slug:        cfg.Slug,
				Tier:        opts.tier(),
				Environment: opts.environment,
				Publisher:   publisher,
				Published:   written,
			})
			return err
		})
		if err != nil {
			return err
		}
		if dependencies.Presentation(stdout).Format == terminal.FormatJSON {
			result := &resultv1.EnvUnpublishResult{Tier: opts.tier(), Publisher: publisher}
			for _, removed := range resp.GetRemoved() {
				result.Removed = append(result.Removed, newResultCoordinate(removed))
			}
			return terminal.WriteResultJSON(stdout, result)
		}
		keys := make([]string, 0, len(resp.GetRemoved()))
		for _, at := range resp.GetRemoved() {
			keys = append(keys, at.GetKey())
		}
		if len(keys) == 0 {
			fmt.Fprintf(stdout, "Removed nothing: %s has written every value again since.\n", publisher)
			return nil
		}
		fmt.Fprintf(stdout, "Removed %s, which %s published.\n", strings.Join(keys, ", "), publisher)
		return nil
	})
}

func decodePublishedVersions(stdin io.Reader) (*variablestorev1.PublishedVersions, error) {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return nil, fmt.Errorf("read the versions on stdin: %w", err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("nothing came in on stdin; `ocel env unpublish` reads the versions one program wrote as protobuf JSON, so pipe them in: `ocel env unpublish --publisher <ID> < versions.json`")
	}
	written := &variablestorev1.PublishedVersions{}
	if err := protojson.Unmarshal(raw, written); err != nil {
		return nil, errors.New(`the versions on stdin do not have the shape {"values": [{"key": <KEY>, "version": <VERSION>}]}`)
	}
	return written, nil
}
