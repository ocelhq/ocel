package provider_test

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestAnAbsentEngineRefusesEveryEngineCallAsNeedingARunner(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	var stacks provider.Stacks = provider.AbsentEngine{}
	var runtime provider.Runtime = provider.AbsentEngine{}

	calls := map[string]func() error{
		"Plan": func() error { _, err := stacks.Plan(ctx, provider.StackSpec{}, nil); return err },
		"Provision": func() error {
			_, err := stacks.Provision(ctx, provider.StackSpec{}, nil)
			return err
		},
		"PlanDestroy": func() error { _, err := stacks.PlanDestroy(ctx, provider.StackRef{}, nil); return err },
		"Destroy":     func() error { return stacks.Destroy(ctx, provider.StackDestroy{}, nil) },
		"Resources": func() error {
			_, err := stacks.Resources(ctx, provider.StackResources{})
			return err
		},
		"Arch":   func() error { _, err := runtime.Arch(ctx, "web", ""); return err },
		"Binary": func() error { _, err := runtime.Binary(ctx, "arm64"); return err },
		"FunctionBase": func() error {
			_, err := runtime.FunctionBase(ctx, buildoutput.Framework{})
			return err
		},
		"FunctionFiles": func() error {
			_, err := runtime.FunctionFiles(ctx, buildoutput.Framework{})
			return err
		},
	}
	for name, call := range calls {
		code, refused := provider.RefusedCode(call())
		if !refused || code != refusal.CodeNeedsRunner {
			t.Errorf("AbsentEngine.%s refused with %q, %v, want %q", name, code, refused, refusal.CodeNeedsRunner)
		}
	}

	if paths := runtime.FunctionBuildPaths(buildoutput.Framework{}); paths != nil {
		t.Errorf("AbsentEngine.FunctionBuildPaths() = %v, want none", paths)
	}
}
