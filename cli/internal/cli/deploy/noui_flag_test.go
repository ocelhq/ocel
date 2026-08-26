package deploy

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/spf13/cobra"
)

func TestNoUIFlag(t *testing.T) {
	t.Run("deploy and preview accept --no-ui", func(t *testing.T) {
		commands := map[string]*cobra.Command{
			"deploy":  NewCommand(cmddeps.Deps{}),
			"preview": NewPreviewCommand(cmddeps.Deps{}),
		}
		for _, want := range []string{"deploy", "preview", "preview up"} {
			t.Run(want, func(t *testing.T) {
				parts := strings.Fields(want)
				target := commands[parts[0]]
				for _, part := range parts[1:] {
					next, _, err := target.Find([]string{part})
					if err != nil || next == target {
						t.Fatalf("command %q not found: %v", want, err)
					}
					target = next
				}
				if target.Flags().Lookup("no-ui") == nil {
					t.Errorf("`ocel %s` does not accept --no-ui", want)
				}
			})
		}
	})
}
