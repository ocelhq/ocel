package deployreport

import (
	"errors"
	"strings"
	"testing"
	"time"

	"buf.build/go/protovalidate"

	"github.com/ocelhq/ocel/cli/internal/project"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

const (
	traceID       = "4bf92f3577b34da6a3ce929d0e0e4736"
	webRelease    = "3f7c1b9a5e2d4c8f~0123456789ab"
	targetAccount = "aws/123456789012"
)

var (
	startedAt  = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	finishedAt = startedAt.Add(time.Minute)
)

func productionAttempt() Attempt {
	return Attempt{
		Kind:        consolev1.DeploymentKind_DEPLOYMENT_KIND_DEPLOY,
		Project:     &project.Project{Slug: "shop", Provider: &project.Provider{ID: "aws"}},
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
		Target:      targetAccount,
		TraceID:     traceID,
		StartedAt:   startedAt,
		Trigger:     &consolev1.Trigger{Kind: consolev1.TriggerKind_TRIGGER_KIND_CLI},
	}
}

func liveApps() []*consolev1.App {
	return []*consolev1.App{{
		Name:    "web",
		BuildId: "3f7c1b9a5e2d4c8f",
		Release: webRelease,
		Compute: consolev1.ComputeKind_COMPUTE_KIND_SERVERLESS,
		Outcome: consolev1.AppOutcome_APP_OUTCOME_SUCCEEDED,
		Urls:    []string{"https://shop.example.com"},
	}}
}

func livePromotion() *consolev1.Promotion {
	return &consolev1.Promotion{Id: "p_01", Seq: 1791374400, Tag: "v1.2.0"}
}

func productionDeployment() *consolev1.Deployment {
	return productionAttempt().Succeeded(finishedAt, liveApps(), livePromotion())
}

func refuse(t *testing.T, deployment *consolev1.Deployment) {
	t.Helper()
	if err := protovalidate.Validate(deployment); err != nil {
		t.Fatalf("the record is one the console refuses: %v", err)
	}
}

func TestASucceededAttemptIsADeploymentRecordTheConsoleAccepts(t *testing.T) {
	t.Parallel()

	deployment := productionDeployment()

	refuse(t, deployment)
	if deployment.GetId() != traceID {
		t.Errorf("id = %q, want the run's trace id %q", deployment.GetId(), traceID)
	}
	if deployment.GetOutcome() != consolev1.DeploymentOutcome_DEPLOYMENT_OUTCOME_SUCCEEDED {
		t.Errorf("outcome = %v, want succeeded", deployment.GetOutcome())
	}
	if deployment.GetPromotion().GetId() != "p_01" || deployment.GetPromotion().GetTag() != "v1.2.0" {
		t.Errorf("promotion = %v, want p_01 tagged v1.2.0", deployment.GetPromotion())
	}
	if deployment.GetPromotion().GetSeq() != 1791374400 {
		t.Errorf("promotion seq = %d, want the router's ts for the promotion, 1791374400", deployment.GetPromotion().GetSeq())
	}
	if deployment.GetProvider().GetName() != "aws" || deployment.GetTarget() != targetAccount {
		t.Errorf("provider %q target %q, want aws and %q", deployment.GetProvider().GetName(), deployment.GetTarget(), targetAccount)
	}
	if !deployment.GetStartedAt().AsTime().Equal(startedAt) || !deployment.GetFinishedAt().AsTime().Equal(finishedAt) {
		t.Errorf("ran %v to %v, want %v to %v", deployment.GetStartedAt().AsTime(), deployment.GetFinishedAt().AsTime(), startedAt, finishedAt)
	}
	if deployment.GetCliVersion() == "" {
		t.Error("cli version is empty, want the version that made the deployment")
	}
}

func TestAFailedAttemptNamesItsErrorAndMadeNothingLive(t *testing.T) {
	t.Parallel()

	deployment := productionAttempt().Failed(finishedAt, liveApps(), errors.New("the stack update failed"))

	refuse(t, deployment)
	if deployment.GetOutcome() != consolev1.DeploymentOutcome_DEPLOYMENT_OUTCOME_FAILED {
		t.Errorf("outcome = %v, want failed", deployment.GetOutcome())
	}
	if deployment.GetPromotion() != nil {
		t.Errorf("promotion = %v, want none: a failed deployment made nothing live", deployment.GetPromotion())
	}
	if deployment.GetError() != "the stack update failed" {
		t.Errorf("error = %q, want the failure", deployment.GetError())
	}
}

func TestAFailedAttemptKeepsTheTailOfAnErrorTooLongForTheRecord(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", 5000) + "the cause"
	deployment := productionAttempt().Failed(finishedAt, liveApps(), errors.New(long))

	refuse(t, deployment)
	if !strings.HasSuffix(deployment.GetError(), "the cause") {
		t.Errorf("error ends %q, want it to keep the end of the message, where the cause is", deployment.GetError()[len(deployment.GetError())-20:])
	}
}

func TestAnAttemptThatFinishedBeforeItStartedIsClampedToItsStart(t *testing.T) {
	t.Parallel()

	deployment := productionAttempt().Succeeded(startedAt.Add(-time.Hour), liveApps(), livePromotion())

	refuse(t, deployment)
}

func TestAPreviewUpAttemptRecordsThePreviewEnvironmentAndItsEdge(t *testing.T) {
	t.Parallel()
	attempt := productionAttempt()
	attempt.Kind = consolev1.DeploymentKind_DEPLOYMENT_KIND_PREVIEW_UP
	attempt.Environment = &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-12"}

	deployment := attempt.Succeeded(finishedAt, liveApps(), &consolev1.Promotion{Id: "p_01", Seq: 1791374400})

	refuse(t, deployment)
	if deployment.GetEnvironment().GetIdentity() != "pr-12" {
		t.Errorf("environment = %v, want preview pr-12", deployment.GetEnvironment())
	}
}

func TestAnEnvironmentEventIsARecordTheConsoleAccepts(t *testing.T) {
	t.Parallel()
	inCI := environment(map[string]string{"CI": "true"})

	preview := NewEnvironmentEvent(consolev1.EnvironmentEventKind_ENVIRONMENT_EVENT_KIND_PREVIEW_REMOVED,
		&environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-12"}, traceID, finishedAt, t.TempDir(), inCI)
	destroyed := NewEnvironmentEvent(consolev1.EnvironmentEventKind_ENVIRONMENT_EVENT_KIND_DESTROYED,
		&environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION}, traceID, finishedAt, t.TempDir(), environment(nil))

	for _, event := range []*consolev1.EnvironmentEvent{preview, destroyed} {
		if err := protovalidate.Validate(event); err != nil {
			t.Errorf("event %v is one the console refuses: %v", event, err)
		}
	}
	if preview.GetId() != traceID || !preview.GetAt().AsTime().Equal(finishedAt) || preview.GetCi().GetName() != "ci" {
		t.Errorf("event = %v, want the run's trace id, its time and its CI", preview)
	}
	if destroyed.GetCi() != nil || destroyed.GetSource() != nil {
		t.Errorf("event = %v, want no CI outside one and no source outside git", destroyed)
	}
}
