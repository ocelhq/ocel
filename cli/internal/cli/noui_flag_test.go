package cli

import (
	"strings"
	"testing"
)

func TestNoUIFlag_RegisteredOnDeployAndPreview(t *testing.T) {
	for _, cmd := range []struct {
		name string
		want string
	}{
		{"deploy", "deploy"},
		{"preview", "preview"},
		{"preview up", "preview up"},
	} {
		t.Run(cmd.name, func(t *testing.T) {
			target := rootCmd
			for _, part := range strings.Fields(cmd.want) {
				next, _, err := target.Find([]string{part})
				if err != nil || next == target {
					t.Fatalf("command %q not found: %v", cmd.want, err)
				}
				target = next
			}
			if target.Flags().Lookup("no-ui") == nil {
				t.Errorf("`ocel %s` does not accept --no-ui", cmd.want)
			}
		})
	}
}
