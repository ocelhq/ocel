package providerserver_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func assertStagesClose(t *testing.T, events []*progressv1.OperationEvent) {
	t.Helper()

	titles := map[string]string{}
	var order []string
	ended := map[string]int{}
	for _, event := range events {
		key := string(event.GetSpanId())
		if event.GetStarted() != nil {
			if _, seen := titles[key]; seen {
				t.Errorf("scope %q is started twice", event.GetMessage())
				continue
			}
			titles[key] = event.GetMessage()
			order = append(order, key)
		}
		if event.GetEnded() != nil {
			if _, started := titles[key]; !started {
				t.Errorf("an ended event names span %x, which no started event opened", event.GetSpanId())
			}
			ended[key]++
		}
	}
	if len(order) == 0 {
		t.Fatal("the run started no scope at all")
	}

	for _, key := range order {
		if ended[key] != 1 {
			t.Errorf("scope %q is ended %d times, want every scope a run opens ended exactly once", titles[key], ended[key])
		}
	}
}

func saidLine(event *progressv1.OperationEvent) string {
	if event.GetBody() != nil || event.GetLevel() != progressv1.Level_LEVEL_INFO {
		return ""
	}
	return event.GetMessage()
}

type startedScope struct {
	id, parent, title string
	phase             progressv1.Phase
}

func startedScopes(events []*progressv1.OperationEvent) []startedScope {
	var out []startedScope
	for _, event := range events {
		if started := event.GetStarted(); started != nil {
			out = append(out, startedScope{
				id:     string(event.GetSpanId()),
				parent: string(started.GetParentSpanId()),
				title:  event.GetMessage(),
				phase:  event.GetPhase(),
			})
		}
	}
	return out
}

func recorded(stream *connect.ServerStreamForClient[progressv1.OperationEvent]) []*progressv1.OperationEvent {
	defer stream.Close()
	var events []*progressv1.OperationEvent
	for stream.Receive() {
		events = append(events, stream.Msg())
	}
	return events
}

func TestEveryScopeADeployOpensIsEndedExactlyOnce(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	result, events := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	assertStagesClose(t, events)
}

func TestDeployStartsEveryUnitAndItsPhasesBeforeAnyScopeEnds(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	result, events := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	var roster []string
	phases := map[string][]string{}
	units := map[string]string{}
	ending := false
	for _, event := range events {
		if event.GetEnded() != nil {
			ending = true
		}
		started := event.GetStarted()
		if started == nil {
			continue
		}
		parent := string(started.GetParentSpanId())
		if parent == "" {
			if ending {
				t.Errorf("unit %q starts after a scope ended, want every unit on the spine named up front", event.GetMessage())
			}
			units[string(event.GetSpanId())] = event.GetMessage()
			roster = append(roster, event.GetMessage())
			continue
		}
		unit, isUnit := units[parent]
		if !isUnit {
			continue
		}
		if ending {
			t.Errorf("phase %q starts after a scope ended, want every phase named with the unit that runs it", event.GetMessage())
		}
		phases[unit] = append(phases[unit], event.GetMessage())
	}

	want := []string{"Environment", "Shared infrastructure", "web", "Edge", "Hostnames", "Promotion"}
	if strings.Join(roster, ",") != strings.Join(want, ",") {
		t.Errorf("roster = %v, want %v", roster, want)
	}
	if got := strings.Join(phases["Environment"], ","); got != "Provisioning" {
		t.Errorf("Environment starts the phases %q, want it named before the first one closes", got)
	}
}

func phasesUnder(events []*progressv1.OperationEvent, unit string) []progressv1.Phase {
	units := map[string]string{}
	var phases []progressv1.Phase
	for _, scope := range startedScopes(events) {
		if scope.parent == "" {
			units[scope.id] = scope.title
			continue
		}
		if units[scope.parent] == unit {
			phases = append(phases, scope.phase)
		}
	}
	return phases
}

func TestADeploysPromotionRunsInThePromotePhase(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	result, events := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	got := phasesUnder(events, "Promotion")
	if len(got) != 1 || got[0] != progressv1.Phase_PHASE_PROMOTE {
		t.Errorf("the Promotion unit runs in %v, want the promote phase", got)
	}
}

func TestAnAppUnitsEventsNameTheAppAsSubjectInTheDeployPhase(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	result, events := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	if got := phasesUnder(events, "web"); len(got) != 1 || got[0] != progressv1.Phase_PHASE_DEPLOY {
		t.Errorf("the web unit runs in %v, want the deploy phase", got)
	}

	app := map[string]bool{}
	for _, scope := range startedScopes(events) {
		if scope.title == "web" || app[scope.parent] {
			app[scope.id] = true
		}
	}
	var scoped int
	for _, event := range events {
		if !app[string(event.GetSpanId())] {
			continue
		}
		scoped++
		if event.GetStarted() != nil && len(event.GetStarted().GetParentSpanId()) == 0 {
			if event.GetSubject() != "web" {
				t.Errorf("the web unit starts naming %q, want \"web\"", event.GetSubject())
			}
			continue
		}
		if event.GetSubject() != "web" || event.GetPhase() != progressv1.Phase_PHASE_DEPLOY {
			t.Errorf("an event of the web unit is scoped %q in %v, want \"web\" in the deploy phase", event.GetSubject(), event.GetPhase())
		}
	}
	if scoped == 0 {
		t.Fatal("no event names the web unit's span, want its progress and spans scoped to it")
	}
}

func TestTheEdgeUnitsEventsNameTheEdgeKindAsSubject(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	result, events := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	unit := map[string]bool{}
	for _, scope := range startedScopes(events) {
		if scope.title == "Edge" || unit[scope.parent] {
			unit[scope.id] = true
		}
	}
	var scoped int
	for _, event := range events {
		if !unit[string(event.GetSpanId())] {
			continue
		}
		scoped++
		if event.GetSubject() != string(fake.KindRelay) {
			t.Errorf("an event of the Edge unit names %q, want the edge it deploys, %q", event.GetSubject(), fake.KindRelay)
		}
	}
	if scoped == 0 {
		t.Fatal("no event names the Edge unit's span, want its progress and spans scoped to it")
	}
}

func TestARemovalRunsInTheDestroyPhase(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	deployed(t, vendor, edge.ClassPreview, "shop")

	stream, err := client.RemoveEnvironment(context.Background(), &contractv1.RemoveEnvironmentRequest{
		Slug:        "shop",
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-7"},
	})
	if err != nil {
		t.Fatalf("RemoveEnvironment() error = %v", err)
	}
	got := phasesUnder(recorded(stream), "Environment")
	if len(got) != 1 || got[0] != progressv1.Phase_PHASE_DESTROY {
		t.Errorf("the removal runs in %v, want the destroy phase", got)
	}
}

func TestBootstrapEndsEveryScopeItStarts(t *testing.T) {
	t.Run("when the work succeeds", func(t *testing.T) {
		t.Parallel()
		client, _ := contractServed(t, "1.0.0")

		stream, err := client.Bootstrap(context.Background(), &contractv1.BootstrapRequest{
			Tier: environmentv1.Tier_TIER_PRODUCTION,
		})
		if err != nil {
			t.Fatalf("Bootstrap() error = %v", err)
		}
		assertStagesClose(t, recorded(stream))
	})

	t.Run("when the work fails", func(t *testing.T) {
		t.Parallel()
		client, provider := contractServed(t, "1.0.0")
		provider.FakeBootstrap().RefuseApply(errors.New("the bootstrap fell over"))

		stream, err := client.Bootstrap(context.Background(), &contractv1.BootstrapRequest{
			Tier: environmentv1.Tier_TIER_PRODUCTION,
		})
		if err != nil {
			t.Fatalf("Bootstrap() error = %v", err)
		}
		events := recorded(stream)
		if len(events) == 0 {
			t.Fatal("a failed Bootstrap() streamed nothing at all")
		}
		assertStagesClose(t, events)

		titles := map[string]string{}
		for _, scope := range startedScopes(events) {
			titles[scope.id] = scope.title
		}
		for _, event := range events {
			ended := event.GetEnded()
			if ended == nil {
				continue
			}
			if ended.GetStatus() != progressv1.SpanStatus_SPAN_STATUS_ERROR {
				t.Errorf("the scope %q ends %v, want ERROR: the work under it failed", titles[string(event.GetSpanId())], ended.GetStatus())
			}
		}
	})
}
