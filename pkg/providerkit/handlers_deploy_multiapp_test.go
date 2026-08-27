package providerkit_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
)

const promotionUnitSpan = "Promotion"

const webRefusal = "the web stack would not stand up"

func appBarrier(t *testing.T, width int) func(providerkit.StackPlan) error {
	t.Helper()
	var mu sync.Mutex
	var arrived int
	gate := make(chan struct{})
	return func(plan providerkit.StackPlan) error {
		if plan.App == nil {
			return nil
		}
		mu.Lock()
		arrived++
		full := arrived == width
		mu.Unlock()
		if full {
			close(gate)
		}
		select {
		case <-gate:
			return nil
		case <-time.After(5 * time.Second):
			return fmt.Errorf("%s waited for %d apps to be provisioning at once and only %d ever were", plan.App.App, width, arrived)
		}
	}
}

func spanStatuses(events []*progressv1.OperationEvent) map[string]progressv1.SpanStatus {
	statuses := map[string]progressv1.SpanStatus{}
	for _, event := range events {
		if span := event.GetSpan(); span != nil {
			statuses[span.GetName()] = span.GetStatus()
		}
	}
	return statuses
}

func outcomes(result *progressv1.ResultEvent) []string {
	reported := make([]string, 0, len(result.GetApps()))
	for _, app := range result.GetApps() {
		reported = append(reported, app.GetApp()+"="+app.GetOutcome().String())
	}
	return reported
}

func TestDeployProvisionsAppsAtTheSameTime(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)
	provider.Releases().(*fake.Releaser).Entering(appBarrier(t, 2))

	result, _ := deploy(t, client, twoAppRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want two apps standing up at once and the deploy succeeding", result.GetError())
	}
}

func TestDeployFinishesASiblingOfAFailedAppAndWithholdsPromotion(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)

	webFailed := make(chan struct{})
	provider.Releases().(*fake.Releaser).Entering(func(plan providerkit.StackPlan) error {
		if plan.App == nil {
			return nil
		}
		if plan.App.App == "web" {
			close(webFailed)
			return errors.New(webRefusal)
		}
		select {
		case <-webFailed:
			return nil
		case <-time.After(5 * time.Second):
			return errors.New("admin gave up waiting for web to fail")
		}
	})

	result, events := deploy(t, client, twoAppRequest())
	if result == nil || result.GetSuccess() {
		t.Fatalf("Deploy() succeeded, want it to fail: one of its two apps did not stand up")
	}

	statuses := spanStatuses(events)
	if got := statuses["web"]; got != progressv1.SpanStatus_SPAN_STATUS_ERROR {
		t.Errorf("the web unit span is %s, want SPAN_STATUS_ERROR", got)
	}
	if got := statuses["admin"]; got != progressv1.SpanStatus_SPAN_STATUS_OK {
		t.Errorf("the admin unit span is %s, want SPAN_STATUS_OK: a sibling of a failed app runs to completion", got)
	}
	if _, promoted := statuses[promotionUnitSpan]; promoted {
		t.Error("the run promoted, want promotion withheld: it is withheld for every app unless every app succeeds")
	}

	want := []string{"web=APP_OUTCOME_FAILED", "admin=APP_OUTCOME_SUCCEEDED"}
	if got := outcomes(result); !slices.Equal(got, want) {
		t.Fatalf("the result reports %v, want %v", got, want)
	}
	if detail := result.GetApps()[0].GetError(); !strings.Contains(detail, webRefusal) {
		t.Errorf("web's outcome carries %q, want the failure that stopped it", detail)
	}
}

func TestDeployReportsAppOutcomesInManifestOrderWhicheverFinishesFirst(t *testing.T) {
	for _, first := range []string{"web", "admin"} {
		t.Run(first+" finishes first", func(t *testing.T) {
			builtProject(t)
			client, provider := deployServed(t)

			done := make(chan struct{})
			provider.Releases().(*fake.Releaser).Entering(func(plan providerkit.StackPlan) error {
				if plan.App == nil {
					return nil
				}
				if plan.App.App == first {
					close(done)
					return nil
				}
				select {
				case <-done:
				case <-time.After(5 * time.Second):
				}
				return nil
			})

			result, _ := deploy(t, client, twoAppRequest())
			if result == nil || !result.GetSuccess() {
				t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
			}
			want := []string{"web=APP_OUTCOME_SUCCEEDED", "admin=APP_OUTCOME_SUCCEEDED"}
			if got := outcomes(result); !slices.Equal(got, want) {
				t.Fatalf("the result reports %v, want %v: the order is the manifest's, not the finishing order", got, want)
			}
		})
	}
}

func TestDeployStartsNoAppWhenTheSharedInfrastructureFails(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)

	provider.Releases().(*fake.Releaser).Entering(func(plan providerkit.StackPlan) error {
		if plan.App == nil {
			return errors.New("the environment's infrastructure would not stand up")
		}
		return errors.New("an app was provisioned over infrastructure that never stood up")
	})

	result, events := deploy(t, client, twoAppRequest())
	if result == nil || result.GetSuccess() {
		t.Fatalf("Deploy() succeeded, want it to fail: its shared infrastructure did not stand up")
	}
	statuses := spanStatuses(events)
	for _, app := range []string{"web", "admin"} {
		if _, ran := statuses[app]; ran {
			t.Errorf("%s ran, want no app started at all once the infrastructure it links to failed", app)
		}
	}
	want := []string{"web=APP_OUTCOME_NOT_RUN", "admin=APP_OUTCOME_NOT_RUN"}
	if got := outcomes(result); !slices.Equal(got, want) {
		t.Fatalf("the result reports %v, want %v", got, want)
	}
}
