package previewprotection

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
)

func newSyncCommand(invocation commands.Invocation) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Publish this project's recorded preview credentials to its edge again",
		Long: "Publish this project's recorded preview credentials to its edge again.\n\n" +
			"Every change is recorded first and then published to the edge that serves the project's previews. " +
			"When an edge could not be reached the change stays recorded, `ocel preview protection` shows the edge as " +
			"pending, and this publishes the record again. It is safe to run any time, and changes nothing the " +
			"edge already holds.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			outcome, err := applyChange(cmd.Context(), invocation, "ocel preview protection sync", nil,
				func(outcome changeOutcome) string {
					if len(outcome.published.GetEdges()) == 0 {
						return fmt.Sprintf("Nothing to publish for %s: no edge serves its previews yet", outcome.slug)
					}
					return fmt.Sprintf("Published the preview credentials of %s to its edge", outcome.slug)
				})
			return printPublication(invocation, cmd.OutOrStdout(), outcome, err)
		},
	}
	return commands.DeclareResult(commands.DeclareMutating(commands.PrintOnlyUnderJSON(cmd)), &resultv1.PreviewPublicationResult{})
}
