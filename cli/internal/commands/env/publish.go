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
	"github.com/ocelhq/ocel/cli/internal/variables"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
)

func newPublishCommand(dependencies Dependencies) *cobra.Command {
	var opts envOptions
	var publisher string
	cmd := &cobra.Command{
		Use:   "publish",
		Short: "Publish the values one infrastructure program writes, read as JSON on stdin",
		Long: "Publish the values one infrastructure program writes, read as JSON on stdin.\n\n" +
			"The input is a provider.variablestore.v1.PublishedValues in protobuf JSON, every value the " +
			"publisher writes to this tier or preview environment:\n\n" +
			"  {\"values\": [{\"key\": \"API_KEY\", \"value\": \"…\", \"secret\": true}]}\n\n" +
			"Each value is checked as `ocel env set` checks one: a secret value must be declared secret, " +
			"and a plain value must not be. " +
			"A value the publisher wrote before and no longer lists is removed, so publishing nothing removes " +
			"everything it wrote. A value a publisher wrote is changed only by publishing again: " +
			"`ocel env set` and `ocel env rm` refuse it.",
		Example: "  $ ocel env publish --publisher urn:pulumi:prod::infra < values.json",
		Args:    cobra.NoArgs,
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return withCommand(cmd, dependencies, func(ctx context.Context, cwd string) error {
			return runEnvPublish(ctx, dependencies, cwd, publisher, cmd.InOrStdin(), opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	}
	previewFlag(cmd, &opts)
	environmentFlag(cmd, &opts)
	cmd.Flags().StringVar(&publisher, "publisher", "", "The stable `id` of the program publishing these values")
	_ = cmd.MarkFlagRequired("publisher")
	return commands.DeclareResult(commands.DeclareMutating(cmd), &resultv1.EnvPublishResult{})
}

func runEnvPublish(ctx context.Context, dependencies Dependencies, cwd, publisher string, stdin io.Reader, opts envOptions, stdout, stderr io.Writer) error {
	published, err := decodePublishedValues(stdin)
	if err != nil {
		return err
	}
	return withEnvProvider(ctx, dependencies, cwd, opts, "ocel env publish", []cloudv1.Scope{cloudv1.Scope_SCOPE_VARIABLES_WRITE}, stderr, func(ctx context.Context, run *run.Run, provider *providerprocess.Provider, cfg *project.Project, _ *contractv1.PreflightResponse) error {
		if len(published.GetValues()) > 0 {
			definitions, _, err := declaredVariables(ctx, dependencies, cfg, provider, published.GetValues()[0].GetKey(), opts, run)
			if err != nil {
				return err
			}
			for _, value := range published.GetValues() {
				if err := variables.RefuseUnwritable(definitions, value.GetKey(), ""); err != nil {
					return err
				}
				row := variables.ValueMetadata{Coordinate: envCoordinate(value.GetKey(), opts), Publisher: publisher, PublishedSecret: value.GetSecret()}
				if err := variables.RefusePublishedClass(definitions, row); err != nil {
					return err
				}
			}
		}
		variableStore, err := provider.VariableStore()
		if err != nil {
			return err
		}
		resp, err := variableStore.PublishValues(ctx, &variablestorev1.PublishValuesRequest{
			Slug:        cfg.Slug,
			Tier:        opts.tier(),
			Environment: opts.environment,
			Publisher:   publisher,
			Published:   published,
		})
		if err != nil {
			return err
		}
		if dependencies.Presentation(stdout).Format == terminal.FormatJSON {
			result := &resultv1.EnvPublishResult{Tier: opts.tier(), Publisher: publisher}
			for _, written := range resp.GetWritten() {
				result.Written = append(result.Written, &resultv1.EnvWrittenValue{Coordinate: newResultCoordinate(written.GetCoordinate()), Version: written.GetVersion()})
			}
			for _, removed := range resp.GetRemoved() {
				result.Removed = append(result.Removed, newResultCoordinate(removed))
			}
			return terminal.WriteResultJSON(stdout, result)
		}
		fmt.Fprintf(stdout, "Published %d values as %s.\n", len(resp.GetWritten()), publisher)
		if removed := resp.GetRemoved(); len(removed) > 0 {
			keys := make([]string, 0, len(removed))
			for _, at := range removed {
				keys = append(keys, at.GetKey())
			}
			fmt.Fprintf(stdout, "Removed %s, which it no longer publishes.\n", strings.Join(keys, ", "))
		}
		return nil
	})
}

func decodePublishedValues(stdin io.Reader) (*variablestorev1.PublishedValues, error) {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return nil, fmt.Errorf("read the values on stdin: %w", err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("nothing came in on stdin; `ocel env publish` reads the values one program publishes as protobuf JSON, so pipe them in: `ocel env publish --publisher <ID> < values.json`")
	}
	published := &variablestorev1.PublishedValues{}
	if err := protojson.Unmarshal(raw, published); err != nil {
		return nil, errors.New(`the values on stdin do not have the shape {"values": [{"key": <KEY>, "value": <TEXT>, "secret": <true or false>}]}`)
	}
	return published, nil
}
