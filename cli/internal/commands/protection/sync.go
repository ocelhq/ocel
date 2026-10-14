package protection

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
)

func newSyncCommand(invocation commands.Invocation) *cobra.Command {
	var t target
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Publish the recorded credentials to the edge again",
		Long: "Publish this project's recorded production credentials, or its preview ones with `--preview`, to its edge again.\n\n" +
			"Every change is recorded first and then published to the edge that serves the tier. " +
			"When an edge could not be reached the change stays recorded, `ocel protection` shows the edge as " +
			"pending, and this publishes the record again. It is safe to run any time, and changes nothing the " +
			"edge already holds.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			outcome, err := applyChange(cmd.Context(), invocation, t, "ocel protection sync", nil,
				func(outcome changeOutcome) string {
					if len(outcome.published.GetEdges()) == 0 {
						return fmt.Sprintf("Nothing to publish for %s: no edge serves its %s yet", outcome.slug, t.name())
					}
					return fmt.Sprintf("Published the %s credentials of %s to its edge", t.name(), outcome.slug)
				})
			return printPublication(invocation, cmd.OutOrStdout(), outcome, err)
		},
	}
	t.bind(cmd)
	return commands.DeclareResult(commands.DeclareMutating(commands.PrintOnlyUnderJSON(cmd)), &resultv1.PublicationResult{})
}
