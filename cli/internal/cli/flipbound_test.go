package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

var flipBoundCases = []struct {
	name  string
	spec  string
	want  string
	other []string
}{
	{
		name:  "instant",
		spec:  "0",
		other: []string{"propagates"},
	},
	{
		name:  "published",
		spec:  "5000:published",
		want:  "propagates within ~5 s",
		other: []string{"typical, not guaranteed"},
	},
	{
		name:  "unpublished",
		spec:  "5000",
		want:  "propagates in ~5 s (typical, not guaranteed)",
		other: []string{"propagates within"},
	},
}

func TestFlipBoundOnTheRollbackPromotionLine(t *testing.T) {
	for _, tc := range flipBoundCases {
		t.Run(tc.name, func(t *testing.T) {
			root, sockPath := clitest.SetUpDeployFixture(t)
			deps := newDeps()
			clitest.SetLoggedIn(&deps)
			clitest.StubBuild(&deps, nil)
			t.Setenv(clitest.FakeInfraTierEnvVar, "production")
			t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
			t.Setenv(clitest.FakeFlipBoundEnvVar, tc.spec)

			var stdout, stderr bytes.Buffer
			if err := runRollback(context.Background(), deps, root, rollbackOptions{}, &stdout, &stderr); err != nil {
				t.Fatalf("runRollback err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}

			out := stdout.String()
			line := ""
			for _, l := range strings.Split(out, "\n") {
				if strings.HasPrefix(l, "Rolled back to promotion") {
					line = l
				}
			}
			if line == "" {
				t.Fatalf("stdout = %q, want a rolled-back line", out)
			}
			assertFlipNote(t, line, tc.want, tc.other)

			clitest.WaitForNoStaleSocket(t, sockPath)
		})
	}
}

func TestFlipBoundIsAbsentFromThePromotionList(t *testing.T) {
	root, sockPath := clitest.SetUpDeployFixture(t)
	deps := newDeps()
	clitest.SetLoggedIn(&deps)
	clitest.StubBuild(&deps, nil)
	t.Setenv(clitest.FakeInfraTierEnvVar, "production")
	t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
	t.Setenv(clitest.FakeFlipBoundEnvVar, "5000")

	var stdout, stderr bytes.Buffer
	if err := runPromotionsLs(context.Background(), deps, root, &stdout, &stderr); err != nil {
		t.Fatalf("runPromotionsLs err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	if strings.Contains(stdout.String(), "propagates") {
		t.Errorf("stdout = %q, want the flip note only on a promotion line", stdout.String())
	}

	clitest.WaitForNoStaleSocket(t, sockPath)
}

func assertFlipNote(t *testing.T, out, want string, absent []string) {
	t.Helper()
	if want != "" && !strings.Contains(out, want) {
		t.Errorf("output = %q, want it to carry %q", out, want)
	}
	for _, unwanted := range absent {
		if strings.Contains(out, unwanted) {
			t.Errorf("output = %q, want no %q", out, unwanted)
		}
	}
}
