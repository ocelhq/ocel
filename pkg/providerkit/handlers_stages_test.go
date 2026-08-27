package providerkit_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func assertStagesClose(t *testing.T, events []*progressv1.OperationEvent) {
	t.Helper()

	declared := map[string]*progressv1.Stage{}
	var order []string
	var units []string
	spans := map[string]int{}
	for _, event := range events {
		for _, stage := range event.GetStagePlan().GetStages() {
			key := string(stage.GetId())
			if _, seen := declared[key]; seen {
				continue
			}
			declared[key] = stage
			order = append(order, key)
			if len(stage.GetParentId()) == 0 {
				units = append(units, key)
			}
		}
		if span := event.GetSpan(); span != nil {
			spans[string(span.GetSpanId())]++
		}
	}
	if len(declared) == 0 {
		t.Fatal("the run declared no stages at all")
	}

	t.Run("every declared stage is closed by a span", func(t *testing.T) {
		for _, key := range order {
			if spans[key] == 0 {
				t.Errorf("stage %q was declared and no span carries its id: a span whose span_id equals the stage id is the only end-of-stage signal, so this stage never ends", declared[key].GetTitle())
			}
		}
	})

	t.Run("a unit is closed exactly once", func(t *testing.T) {
		for _, key := range units {
			if spans[key] > 1 {
				t.Errorf("unit %q is closed by %d spans, want one span covering the unit's whole extent", declared[key].GetTitle(), spans[key])
			}
		}
	})
}

func recorded(stream *connect.ServerStreamForClient[progressv1.OperationEvent]) []*progressv1.OperationEvent {
	defer stream.Close()
	var events []*progressv1.OperationEvent
	for stream.Receive() {
		events = append(events, stream.Msg())
	}
	return events
}

func TestDeployClosesEveryDeclaredStageAndEachUnitOnce(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	result, events := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	assertStagesClose(t, events)
}

func TestDeployDeclaresItsWholeUnitRosterBeforeAnyPhase(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	result, events := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	var roster []string
	for _, event := range events {
		plan := event.GetStagePlan()
		if plan == nil {
			continue
		}
		if roster == nil {
			for _, stage := range plan.GetStages() {
				if len(stage.GetParentId()) != 0 {
					t.Fatalf("the first stage plan declares phase %q, want the unit roster declared whole before any phase", stage.GetTitle())
				}
				roster = append(roster, stage.GetTitle())
			}
			continue
		}
		for _, stage := range plan.GetStages() {
			if len(stage.GetParentId()) == 0 {
				t.Errorf("unit %q is declared after the roster, want every unit on the spine named up front", stage.GetTitle())
			}
		}
	}

	want := []string{"Environment", "Shared infrastructure", "web", "Edge", "Promotion"}
	if strings.Join(roster, ",") != strings.Join(want, ",") {
		t.Errorf("roster = %v, want %v", roster, want)
	}
}

func TestBootstrapClosesEveryDeclaredStage(t *testing.T) {
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
		provider.Bootstrapper().RefuseApply(errors.New("the bootstrap fell over"))

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

		declared := map[string]bool{}
		for _, event := range events {
			for _, stage := range event.GetStagePlan().GetStages() {
				declared[string(stage.GetId())] = true
			}
		}
		for _, event := range events {
			span := event.GetSpan()
			if span == nil || !declared[string(span.GetSpanId())] {
				continue
			}
			if span.GetStatus() != progressv1.SpanStatus_SPAN_STATUS_ERROR {
				t.Errorf("the span closing %q reports %v, want ERROR: the work under it failed", span.GetName(), span.GetStatus())
			}
		}
	})
}
