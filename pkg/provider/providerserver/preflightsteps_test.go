package providerserver_test

import (
	"context"
	"slices"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func startedSteps(events []*progressv1.OperationEvent) []string {
	var steps []string
	for _, event := range events {
		if event.GetStarted() != nil {
			steps = append(steps, event.GetSubject()+": "+event.GetMessage())
		}
	}
	return steps
}

func TestPreflightReportsEachCheckAsItsOwnStepInTheCheckPhase(t *testing.T) {
	t.Parallel()

	client, _ := contractServed(t, "1.0.0")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})

	_, events, err := preflightWithSteps(context.Background(), client, &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Slug:         "shop",
		Containers:   []*contractv1.ContainerApp{{App: "api"}},
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	want := []string{
		"fake: Checking your credentials",
		"fake: Reading the architecture your containers run on",
		"fake: Reading the production bootstrap",
	}
	if got := startedSteps(events); !slices.Equal(got, want) {
		t.Errorf("steps = %q, want %q", got, want)
	}
	for _, event := range events {
		if event.GetPhase() != progressv1.Phase_PHASE_CHECK {
			t.Errorf("step event %v in %s, want the check phase", event, event.GetPhase())
		}
	}
}

func TestPreflightReadsNoBootstrapOnceCredentialsAreRefused(t *testing.T) {
	t.Parallel()

	client, vendor := contractServed(t, "1.0.0")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	vendor.Credentials().(*fake.Credentials).Deny("the key was revoked")

	resp, events, err := preflightWithSteps(context.Background(), client, &contractv1.PreflightRequest{RequiredTier: environmentv1.Tier_TIER_PRODUCTION})
	if err != nil {
		t.Fatalf("Preflight() error = %v, want the refusal reported as a credential problem", err)
	}
	if len(resp.GetCredentialProblems()) != 1 {
		t.Errorf("credential problems = %v, want the refused key", resp.GetCredentialProblems())
	}
	if got := startedSteps(events); !slices.Equal(got, []string{"fake: Checking your credentials"}) {
		t.Errorf("steps = %q, want the credential check alone", got)
	}
	failed := slices.ContainsFunc(events, func(event *progressv1.OperationEvent) bool {
		return event.GetEnded().GetStatus() == progressv1.SpanStatus_SPAN_STATUS_ERROR
	})
	if !failed {
		t.Errorf("events = %v, want the credential step ended as failed", events)
	}
}
