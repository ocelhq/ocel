package providerkit_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	connect "connectrpc.com/connect"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
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

func queuedAppRequest(apps []string) *contractv1.DeployRequest {
	req := deployRequest()
	manifest := req.GetManifest()
	manifest.Apps = nil
	manifest.Functions = nil
	manifest.Usages = nil
	for slot, app := range apps {
		manifest.Apps = append(manifest.Apps, &contractv1.ManifestApp{
			Name:         app,
			Framework:    "next",
			Compute:      string(providerkit.ComputeServerless),
			DeploymentId: fmt.Sprintf("%032x", slot+1),
		})
		manifest.Functions = append(manifest.Functions, &contractv1.ManifestFunction{
			LogicalName:  app + "-server",
			App:          app,
			Runtime:      "nodejs22.x",
			Handler:      "index.handler",
			ArtifactPath: appArtifactPath(app),
		})
	}
	return req
}

func TestDeployStartsTheAppsStillQueuedWhenAnEarlyAppFails(t *testing.T) {
	apps := make([]string, 0, providerkit.AppConcurrency+2)
	for slot := range cap(apps) {
		apps = append(apps, fmt.Sprintf("app%d", slot))
	}
	queued := apps[providerkit.AppConcurrency:]

	builtApps(t, apps...)
	client, provider := deployServed(t)

	inFlight := appBarrier(t, providerkit.AppConcurrency)
	provider.Releases().(*fake.Releaser).Entering(func(plan providerkit.StackPlan) error {
		if plan.App == nil {
			return nil
		}
		if err := inFlight(plan); err != nil {
			return err
		}
		if plan.App.App == apps[0] {
			return errors.New(webRefusal)
		}
		return nil
	})

	result, events := deploy(t, client, queuedAppRequest(apps))
	if result == nil || result.GetSuccess() {
		t.Fatalf("Deploy() succeeded, want it to fail: one of its apps did not stand up")
	}

	want := make([]string, 0, len(apps))
	for slot, app := range apps {
		outcome := "APP_OUTCOME_SUCCEEDED"
		if slot == 0 {
			outcome = "APP_OUTCOME_FAILED"
		}
		want = append(want, app+"="+outcome)
	}
	if got := outcomes(result); !slices.Equal(got, want) {
		t.Fatalf("the result reports %v, want %v: an app the run had not reached still launches and still reports for itself", got, want)
	}

	statuses := spanStatuses(events)
	for _, app := range queued {
		if got := statuses[app]; got != progressv1.SpanStatus_SPAN_STATUS_OK {
			t.Errorf("%s is %s, want SPAN_STATUS_OK: it had not started when its sibling failed, so it still had to launch", app, got)
		}
	}
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

func TestDeployReportsAppOutcomesWhenAnAppRefusesTheRequest(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	req := twoAppRequest()
	for _, fn := range req.GetManifest().GetFunctions() {
		if fn.GetApp() == "admin" {
			fn.ArtifactPath = ""
		}
	}

	result, _, err := deployStream(t, client, req)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("Deploy() = %v, want it refused: an app names no build artifact", err)
	}
	if !strings.Contains(err.Error(), "names no build artifact") {
		t.Fatalf("Deploy() = %v, want the refusal to name what the manifest is missing", err)
	}
	if result == nil {
		t.Fatal("a refused run reported no per-app outcomes at all, so the sibling that stood up is lost")
	}
	want := []string{"web=APP_OUTCOME_SUCCEEDED", "admin=APP_OUTCOME_FAILED"}
	if got := outcomes(result); !slices.Equal(got, want) {
		t.Fatalf("the result reports %v, want %v", got, want)
	}
}
