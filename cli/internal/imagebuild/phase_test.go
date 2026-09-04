package imagebuild_test

import (
	"encoding/json"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/imagebuild"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

func plannedInPhase(t *testing.T, dir, phase string) builtPlan {
	t.Helper()
	raw, err := imagebuild.Plan(located(t, dir), phase)
	if err != nil {
		t.Fatalf("Plan(%s) = %v", dir, err)
	}
	var plan builtPlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatalf("the plan the frontend reads as railpack-plan.json is not JSON: %v", err)
	}
	return plan
}

func variables(t *testing.T, plan builtPlan, step string) map[string]string {
	t.Helper()
	for _, s := range plan.Steps {
		if s.Name == step {
			return s.Variables
		}
	}
	t.Fatalf("the plan has no %q step; it has %d steps", step, len(plan.Steps))
	return nil
}

func TestTheBuildStepRunsInTheSuppressedPhase(t *testing.T) {
	t.Parallel()

	t.Run("a suppressed deploy builds the image in the phase", func(t *testing.T) {
		t.Parallel()

		plan := plannedInPhase(t, "testdata/plainserver", providerkit.PhaseResourcesSuppressed)

		if got := variables(t, plan, "build")[providerkit.PhaseEnvName]; got != providerkit.PhaseResourcesSuppressed {
			t.Errorf("the build step runs with %s = %q, want %q, so the app's build knows nothing is provisioned", providerkit.PhaseEnvName, got, providerkit.PhaseResourcesSuppressed)
		}
	})

	t.Run("a deploy that provisions builds in no phase", func(t *testing.T) {
		t.Parallel()

		plan := plannedInPhase(t, "testdata/plainserver", "")

		if got, taken := variables(t, plan, "build")[providerkit.PhaseEnvName]; taken {
			t.Errorf("the build step runs with %s = %q, want no phase where everything is provisioned", providerkit.PhaseEnvName, got)
		}
	})
}
