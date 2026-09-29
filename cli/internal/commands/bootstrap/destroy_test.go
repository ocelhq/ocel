package bootstrap

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

func TestBootstrapDestroySendsTheEdgeTheProjectDeclared(t *testing.T) {
	cases := []struct {
		name        string
		declaration string
		want        string
	}{
		{"an omitted edge names none, leaving the provider to choose", "", "kind= "},
		{"a declared api-gateway edge names it", "  edge: \"api-gateway\",\n", "kind=api-gateway"},
		{"a declared relay edge names it", "  edge: \"relay\",\n", "kind=relay"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, journal := clitest.SetUpEdgeFixture(t, tc.declaration)
			invocation := clitest.NewInvocation()

			var stdout, stderr bytes.Buffer
			opts := Options{Yes: true}
			clitest.AttachTerminalSink(invocation, &stdout)
			if err := RunDestroy(context.Background(), invocation, root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, strings.NewReader("")); err != nil {
				t.Fatalf("RunDestroy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}

			got := clitest.ReadJournal(t, journal)
			if len(got) != 2 {
				t.Fatalf("destroy reached the provider %d times, want the plan and the teardown: %v", len(got), got)
			}
			for _, line := range got {
				if !strings.Contains(line, tc.want) {
					t.Errorf("provider saw %q, want %q", line, tc.want)
				}
			}
			if !strings.Contains(stdout.String(), "fronted by the relay edge") {
				t.Errorf("stdout = %q, want the plan to name the edge left installed in the account, not the one this run selected", stdout.String())
			}
		})
	}
}
