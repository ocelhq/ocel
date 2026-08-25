package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func TestFlipNote(t *testing.T) {
	cases := []struct {
		name  string
		bound *progressv1.FlipBound
		want  string
	}{
		{name: "an unrecorded bound says nothing"},
		{name: "an instant flip says nothing", bound: &progressv1.FlipBound{}},
		{
			name:  "a published bound promises the duration",
			bound: &progressv1.FlipBound{TypicalMs: 5000, Published: true},
			want:  "propagates within ~5 s",
		},
		{
			name:  "an unpublished bound qualifies the duration",
			bound: &progressv1.FlipBound{TypicalMs: 5000},
			want:  "propagates in ~5 s (typical, not guaranteed)",
		},
		{
			name:  "the duration comes from the recorded milliseconds",
			bound: &progressv1.FlipBound{TypicalMs: 90000, Published: true},
			want:  "propagates within ~90 s",
		},
		{
			name:  "a fractional second keeps its fraction",
			bound: &progressv1.FlipBound{TypicalMs: 1500},
			want:  "propagates in ~1.5 s (typical, not guaranteed)",
		},
		{
			name:  "a sub-second bound stays in milliseconds",
			bound: &progressv1.FlipBound{TypicalMs: 250, Published: true},
			want:  "propagates within ~250 ms",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := flipNote(tc.bound); got != tc.want {
				t.Errorf("flipNote(%v) = %q, want %q", tc.bound, got, tc.want)
			}
		})
	}
}

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
			deps := newDeps()
			clitest.SetLoggedIn(&deps)
			clitest.StubBuild(&deps, nil)
			t.Setenv(clitest.FakeFlipBoundEnvVar, tc.spec)

			var stdout, stderr bytes.Buffer
			if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
				t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}

			assertFlipNote(t, stdout.String(), tc.want, tc.other)
			waitForNoStaleSocket(t, sockPath)
		})
	}
}

func TestFlipBoundOnThePreviewDeployPromotionLine(t *testing.T) {
	for _, tc := range flipBoundCases {
		t.Run(tc.name, func(t *testing.T) {
			root, sockPath := clitest.SetUpDeployFixture(t)
			deps := newDeps()
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
			waitForNoStaleSocket(t, sockPath)
		})
	}
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

			waitForNoStaleSocket(t, sockPath)
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

	waitForNoStaleSocket(t, sockPath)
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
