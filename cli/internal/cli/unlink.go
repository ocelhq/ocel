package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cloudlink"
)

var unlinkCmd = &cobra.Command{
	Use:   "unlink",
	Short: "Remove this directory's Ocel Cloud link",
	Long: "Removes .ocel/link.json, leaving this working tree associated with no\n" +
		"Ocel Cloud project. Nothing on the control plane is deleted.",
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
	removed, err := cloudlink.Clear(projectDir)
	if err != nil {
		return err
	}
	if !removed {
		fmt.Fprintln(stdout, "This directory isn't linked to an Ocel Cloud project.")
		return nil
	}
	fmt.Fprintln(stdout, "✓ Unlinked.")
	return nil
}
