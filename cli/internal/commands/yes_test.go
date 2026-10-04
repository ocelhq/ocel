package commands_test

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
)

func TestAddDryFlagRegistersAFalseByDefaultDryWithTheGivenDescription(t *testing.T) {
	var dry bool
	cmd := &cobra.Command{Use: "destroy"}

	commands.AddDryFlag(cmd, &dry, "Print what would be destroyed and stop")

	flag := cmd.Flags().Lookup("dry")
	if flag == nil {
		t.Fatal("destroy has no --dry")
	}
	if flag.Value.Type() != "bool" || flag.DefValue != "false" || flag.Shorthand != "" || flag.Usage != "Print what would be destroyed and stop" {
		t.Errorf("--dry = type %s, default %s, shorthand %q, usage %q, want a bool defaulting to false with no shorthand and the given usage", flag.Value.Type(), flag.DefValue, flag.Shorthand, flag.Usage)
	}
	if err := cmd.Flags().Parse([]string{"--dry"}); err != nil || !dry {
		t.Errorf("--dry parsed to %v (err %v), want true", dry, err)
	}
}

func TestACommandWithOnlyADryFlagHasAConfirmationFlag(t *testing.T) {
	var dry bool
	cmd := &cobra.Command{Use: "rollback"}

	commands.AddDryFlag(cmd, &dry, "Print and stop")

	if !commands.HasConfirmationFlag(cmd) {
		t.Errorf("a command with --dry has no confirmation flag")
	}
}

func TestACommandWithOnlyAYesFlagHasAConfirmationFlag(t *testing.T) {
	var yes bool
	cmd := &cobra.Command{Use: "env"}

	commands.AddYesFlag(cmd, &yes)

	if !commands.HasConfirmationFlag(cmd) {
		t.Errorf("a command with --yes has no confirmation flag")
	}
}

func TestACommandWithNeitherYesNorDryHasNoConfirmationFlag(t *testing.T) {
	if commands.HasConfirmationFlag(&cobra.Command{Use: "doctor"}) {
		t.Errorf("a command with neither --yes nor --dry has a confirmation flag")
	}
}
