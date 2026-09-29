package deploy

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func TestDeployChecksCredentialsAndTheProjectsBootstrapAsACheckUnitNamedForItsProviderThenSaysWhoItActsAs(t *testing.T) {
	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)
	clitest.StubBuild(&deps, nil)
	useJSONLogFormat(t, &deps)
	root, _ := clitest.SetUpDeployFixture(t)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(deps, &stdout)
	if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	evs := envelopes(t, stdout.String())
	started := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool {
		return ev.GetStarted() != nil && ev.GetMessage() == "Checking your credentials and the production bootstrap for "+clitest.FixtureSlug
	})
	if started < 0 {
		t.Fatalf("no unit started for the credential check: %s", stdout.String())
	}
	unit := evs[started]
	if unit.GetPhase() != progressv1.Phase_PHASE_CHECK || unit.GetSubject() != "fake" {
		t.Errorf("credential check started in %s naming %q, want the check phase naming the provider %q", unit.GetPhase(), unit.GetSubject(), "fake")
	}
	ended := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool {
		return ev.GetEnded() != nil && bytes.Equal(ev.GetSpanId(), unit.GetSpanId())
	})
	if ended < 0 || evs[ended].GetEnded().GetStatus() != progressv1.SpanStatus_SPAN_STATUS_OK {
		t.Fatalf("the credential check never ended OK: %s", stdout.String())
	}
	identity := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool { return ev.GetIdentity() != nil })
	if identity < started || identity > ended {
		t.Errorf("identity at event %d, credential check from %d to %d: want who the deploy acts as named while the check is still open", identity, started, ended)
	}
	if evs[identity].GetPhase() != progressv1.Phase_PHASE_CHECK {
		t.Errorf("identity in %s, want the check phase", evs[identity].GetPhase())
	}
}

func TestAnUnbootstrappedProductionFailsTheCheckUnitWithTheCommandThatBootstrapsIt(t *testing.T) {
	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)
	clitest.StubBuild(&deps, nil)
	useJSONLogFormat(t, &deps)
	root, _ := clitest.SetUpDeployFixture(t)
	t.Setenv(clitest.FakeInfraPresentEnvVar, "0")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(deps, &stdout)
	if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err == nil {
		t.Fatalf("runDeploy succeeded against no bootstrap: %s", stdout.String())
	}

	evs := envelopes(t, stdout.String())
	check := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool {
		return ev.GetStarted() != nil && strings.HasPrefix(ev.GetMessage(), "Checking your credentials")
	})
	if check < 0 {
		t.Fatalf("no credential check started: %s", stdout.String())
	}
	ended := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool {
		return ev.GetEnded() != nil && bytes.Equal(ev.GetSpanId(), evs[check].GetSpanId())
	})
	if ended < 0 || evs[ended].GetEnded().GetStatus() != progressv1.SpanStatus_SPAN_STATUS_ERROR || !strings.Contains(evs[ended].GetMessage(), "ocel bootstrap production") {
		t.Fatalf("the credential check did not fail naming `ocel bootstrap production`: %s", stdout.String())
	}
}

func TestDeploysEventsAreInTheCheckPhaseThenBuildThenTheProvidersDeployPhases(t *testing.T) {
	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)
	clitest.StubBuild(&deps, nil)
	useJSONLogFormat(t, &deps)
	root, _ := clitest.SetUpDeployFixture(t)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(deps, &stdout)
	if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	var order []progressv1.Phase
	for _, ev := range envelopes(t, stdout.String()) {
		phase := ev.GetPhase()
		if phase == progressv1.Phase_PHASE_UNSPECIFIED || (len(order) > 0 && order[len(order)-1] == phase) {
			continue
		}
		order = append(order, phase)
	}
	deploying := []progressv1.Phase{progressv1.Phase_PHASE_PROVISION, progressv1.Phase_PHASE_DEPLOY, progressv1.Phase_PHASE_PROMOTE}
	if len(order) < 3 || order[0] != progressv1.Phase_PHASE_CHECK || order[1] != progressv1.Phase_PHASE_BUILD {
		t.Fatalf("phases in order %v, want check, then build, then the provider's deploy phases", order)
	}
	for _, phase := range order[2:] {
		if !slices.Contains(deploying, phase) {
			t.Errorf("phases in order %v: %s after the build, want only the provider's deploy phases %v", order, phase, deploying)
		}
	}
}
