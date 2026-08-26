package providerkit_test

import (
	"testing"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func TestDeployClosesEveryDeclaredStageAndEachUnitOnce(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	result, events := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

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
		t.Fatal("the deploy declared no stages at all")
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
