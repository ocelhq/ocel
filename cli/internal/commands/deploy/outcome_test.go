package deploy

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/router"
)

var propagationCases = []struct {
	name        string
	propagation router.Propagation
	want        string
	other       []string
}{
	{
		name:        "instant",
		propagation: router.Propagation{},
		other:       []string{"propagates"},
	},
	{
		name:        "published",
		propagation: router.Propagation{Typical: 5 * time.Second, Published: true},
		want:        "propagates within ~5 s",
		other:       []string{"typical, not guaranteed"},
	},
	{
		name:        "unpublished",
		propagation: router.Propagation{Typical: 5 * time.Second},
		want:        "propagates in ~5 s (typical, not guaranteed)",
		other:       []string{"propagates within"},
	},
}

func propagating(fixture clitest.FakeProject, propagation router.Propagation) {
	fixture.Provider.Routers().(*fake.Routers).DataPlane(fake.RouterRelay).Propagates(propagation)
}

func TestPropagationOnTheProductionDeployPromotionLine(t *testing.T) {
	for _, tc := range propagationCases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := setUpDeployProject(t)
			propagating(fixture, tc.propagation)
			dependencies := newTestDependencies()
			stubBuild(&dependencies, nil)

			var stdout, stderr bytes.Buffer
			clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
			if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
				t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}

			assertPropagationNote(t, stdout.String(), tc.want, tc.other)
		})
	}
}

func TestPropagationOnThePreviewDeployPromotionLine(t *testing.T) {
	for _, tc := range propagationCases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := setUpPreviewProject(t)
			propagating(fixture, tc.propagation)
			dependencies := newTestDependencies()
			stubBuild(&dependencies, nil)
			stubGit(&dependencies, "feature/login", "")

			var stdout, stderr bytes.Buffer
			clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
			if err := runPreviewUp(context.Background(), dependencies, fixture.Root, previewUpOptions{}, &stdout, &stderr, strings.NewReader("")); err != nil {
				t.Fatalf("runPreviewUp err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}

			assertPropagationNote(t, stdout.String(), tc.want, tc.other)
		})
	}
}

func assertPropagationNote(t *testing.T, out, want string, absent []string) {
	t.Helper()
	if want != "" && !strings.Contains(out, want) {
		t.Errorf("output = %q, want it to include %q", out, want)
	}
	for _, unwanted := range absent {
		if strings.Contains(out, unwanted) {
			t.Errorf("output = %q, want no %q", out, unwanted)
		}
	}
}
