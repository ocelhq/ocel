package providerserver_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/edge"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider/fake"
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
	for _, tc := range []struct {
		name     string
		preview  bool
		request  func(t *testing.T, vendor *fake.Provider) *contractv1.DeployRequest
		succeeds bool
	}{
		{name: "a production deploy", request: func(*testing.T, *fake.Provider) *contractv1.DeployRequest { return deployRequest() }, succeeds: true},
		{name: "a dry run", request: func(*testing.T, *fake.Provider) *contractv1.DeployRequest {
			req := deployRequest()
			req.Dry = true
			return req
		}, succeeds: true},
		{name: "a preview deploy", preview: true, request: func(*testing.T, *fake.Provider) *contractv1.DeployRequest { return previewRequest() }, succeeds: true},
		{name: "a deploy that fails attaching its hostnames", request: func(t *testing.T, vendor *fake.Provider) *contractv1.DeployRequest {
			writer, err := vendor.DNS().Open(fake.KindZone, "shop.example", "")
			if err != nil {
				t.Fatal(err)
			}
			writer.(*fake.DNSRecords).Refuse(errors.New("the zone's api answered 500"))
			req := deployRequest()
			req.Edge = writtenBy("shop.example")
			return req
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			builtProject(t)
			client, vendor := deployServed(t)
			if tc.preview {
				previewBootstrapped(t, client)
			}

			result, events := deploy(t, client, tc.request(t, vendor))
			if result.GetSuccess() != tc.succeeds {
				t.Fatalf("Deploy() success = %v (%q), want %v", result.GetSuccess(), result.GetError(), tc.succeeds)
			}
			assertStagesClose(t, events)
		})
	}
}

func TestAUnitStartsWhenItRunsNotWhenTheDeployBegins(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	result, events := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	units := map[string]string{}
	var sequence []string
	for _, event := range events {
		if started := event.GetStarted(); started != nil && len(started.GetParentSpanId()) == 0 {
			units[string(event.GetSpanId())] = event.GetMessage()
			sequence = append(sequence, "started "+event.GetMessage())
		}
		if unit, ok := units[string(event.GetSpanId())]; ok && event.GetEnded() != nil {
			sequence = append(sequence, "ended "+unit)
		}
	}
	hostnamesEnded := slices.IndexFunc(sequence, func(step string) bool { return strings.HasPrefix(step, "ended Attaching") })
	promotionStarted := slices.IndexFunc(sequence, func(step string) bool { return strings.HasPrefix(step, "started Switching traffic") })
	if hostnamesEnded < 0 || promotionStarted < hostnamesEnded {
		t.Errorf("units ran as %v, want Promotion started only after Hostnames ended: a started event says the unit is running", sequence)
	}
}

func TestEveryScopeADeployOpensStartsAndEndsInTheSamePhase(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	result, events := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	started := map[string]*progressv1.OperationEvent{}
	for _, event := range events {
		if event.GetStarted() != nil {
			started[string(event.GetSpanId())] = event
		}
		opened, ok := started[string(event.GetSpanId())]
		if event.GetEnded() == nil || !ok {
			continue
		}
		if opened.GetPhase() != event.GetPhase() {
			t.Errorf("scope %q starts in %v and ends in %v, want one phase for the whole scope", opened.GetMessage(), opened.GetPhase(), event.GetPhase())
		}
	}
}

func unitPhases(events []*progressv1.OperationEvent, title string) []progressv1.Phase {
	var phases []progressv1.Phase
	for _, scope := range startedScopes(events) {
		if scope.parent == "" && strings.HasPrefix(scope.title, title) {
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
	got := unitPhases(events, "Switching traffic")
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
	if got := unitPhases(events, "Deploying the serverless app"); len(got) != 1 || got[0] != progressv1.Phase_PHASE_DEPLOY {
		t.Errorf("the web unit runs in %v, want the deploy phase", got)
	}

	app := map[string]bool{}
	for _, scope := range startedScopes(events) {
		if strings.HasPrefix(scope.title, "Deploying the serverless app") || app[scope.parent] {
			app[scope.id] = true
		}
	}
	var scoped int
	for _, event := range events {
		if !app[string(event.GetSpanId())] {
			continue
		}
		scoped++
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
		if strings.HasPrefix(scope.title, "Reconciling the routes") || unit[scope.parent] {
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
	got := unitPhases(recorded(stream), "Removing the preview environment")
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
