package deploy

import (
	"context"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestAStackTheEngineHoldsButTheRecordsNameNoneOfIsNeverAdopted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	engine := &mockedEngine{outputs: siblingAppOutputs("web")}
	stacks := stacksWith(siblingAppConfig(t, "web"), engine)
	spec := siblingAppSpec(t, "web")
	spec.Recorded = false

	var refused refusal.Refusal
	if _, err := stacks.Plan(ctx, spec, nil); !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Errorf("Plan() of a stack no record names = %v, want it refused: ocel deploys over what it provisioned itself, never over what it finds", err)
	}
	if _, err := stacks.Provision(ctx, spec, nil); !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Errorf("Provision() of a stack no record names = %v, want it refused before the engine writes over it", err)
	}
	if len(engine.ran) != 0 {
		t.Errorf("the engine ran %v, want nothing: the refusal comes before the stack is touched", engine.ran)
	}

	spec.Recorded = true
	if _, err := stacks.Plan(ctx, spec, nil); err != nil {
		t.Errorf("Plan() of the stack its records name = %v, want the plan drawn", err)
	}
}
