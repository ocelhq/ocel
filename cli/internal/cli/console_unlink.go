package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/consolebinding"
)

var consoleUnlinkCmd = &cobra.Command{
	Use:   "unlink",
	Short: "Remove this directory's Ocel console link",
	Long: "Removes .ocel/console.json, leaving this working tree associated with no\n" +
		"console project. Nothing on the control plane is deleted.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		return runConsoleUnlink(cwd, cmd.OutOrStdout())
	},
}

func runConsoleUnlink(projectDir string, stdout io.Writer) error {
	removed, err := consolebinding.Clear(projectDir)
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
