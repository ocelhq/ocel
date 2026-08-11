package cli

import (
	"strings"
	"testing"
)

func TestNoUIFlag(t *testing.T) {
	t.Run("deploy and preview accept --no-ui", func(t *testing.T) {
		for _, want := range []string{"deploy", "preview", "preview up"} {
			t.Run(want, func(t *testing.T) {
				target := rootCmd
				for _, part := range strings.Fields(want) {
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
