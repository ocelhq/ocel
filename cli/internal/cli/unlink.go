package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/console/link"
	"github.com/ocelhq/ocel/pkg/constants"
)

var unlinkCmd = &cobra.Command{
	Use:   "unlink",
	Short: "Remove this directory's Ocel console link",
	Long: "Removes " + constants.ProjectStateDirName + "/console.json, leaving this working tree associated with no\n" +
		"console project. Nothing on the control plane is deleted.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		return runUnlink(cwd, cmd.OutOrStdout())
	},
}

func runUnlink(projectDir string, stdout io.Writer) error {
	removed, err := link.Clear(projectDir)
	if err != nil {
		return err
	}
	if !removed {
		fmt.Fprintln(stdout, "This directory isn't linked to a console project.")
		return nil
	}
	fmt.Fprintln(stdout, "✓ Unlinked.")
	return nil
}
