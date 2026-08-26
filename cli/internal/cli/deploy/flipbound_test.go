package deploy

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

func TestFlipBoundOnTheProductionDeployPromotionLine(t *testing.T) {
	for _, tc := range flipBoundCases {
		t.Run(tc.name, func(t *testing.T) {
			root, sockPath := clitest.SetUpDeployFixture(t)
			deps := clitest.NewDeps()
			clitest.SetLoggedIn(&deps)
			clitest.StubBuild(&deps, nil)
			t.Setenv(clitest.FakeFlipBoundEnvVar, tc.spec)

			var stdout, stderr bytes.Buffer
			if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
				t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}

			assertFlipNote(t, stdout.String(), tc.want, tc.other)
			clitest.WaitForNoStaleSocket(t, sockPath)
		})
	}
}

func TestFlipBoundOnThePreviewDeployPromotionLine(t *testing.T) {
	for _, tc := range flipBoundCases {
		t.Run(tc.name, func(t *testing.T) {
			root, sockPath := clitest.SetUpDeployFixture(t)
			deps := clitest.NewDeps()
			clitest.SetLoggedIn(&deps)
			clitest.StubBuild(&deps, nil)
			stubGit(&deps, "feature/login", "")
			t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
			t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
			t.Setenv(clitest.FakeFlipBoundEnvVar, tc.spec)

			var stdout, stderr bytes.Buffer
			if err := runPreviewUp(context.Background(), deps, root, previewUpOptions{}, &stdout, &stderr, strings.NewReader("")); err != nil {
				t.Fatalf("runPreviewUp err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}

			assertFlipNote(t, stdout.String(), tc.want, tc.other)
			clitest.WaitForNoStaleSocket(t, sockPath)
		})
	}
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
