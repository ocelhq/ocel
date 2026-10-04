package root

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
)

func visibleCommands(root *cobra.Command) []*cobra.Command {
	var found []*cobra.Command
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		for _, sub := range cmd.Commands() {
			if sub.Hidden {
				continue
			}
			found = append(found, sub)
			walk(sub)
		}
	}
	walk(root)
	return found
}

func commandPath(cmd *cobra.Command) string {
	return strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()+" ")
}

func TestEveryVisibleCommandDeclaresWhetherItMutates(t *testing.T) {
	for _, cmd := range visibleCommands(newCommand().root) {
		if _, declared := commands.FindMutation(cmd); !declared {
			t.Errorf("ocel %s does not declare whether it mutates, want commands.Mutates or commands.ReadOnly", commandPath(cmd))
		}
	}
}
