package provider

import (
	"context"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type AbsentEngine struct{}

func RefuseNeedsRunner(call string) error {
	return refusal.Refuse(refusal.CodeNeedsRunner, "this endpoint runs no provisioning engine, so %s runs as a job on a runner", call)
}

func (AbsentEngine) Plan(context.Context, StackSpec, progress.Log) (Plan, error) {
	return Plan{}, RefuseNeedsRunner("planning a stack")
}

func (AbsentEngine) Provision(context.Context, StackSpec, progress.Log) (StackResult, error) {
	return StackResult{}, RefuseNeedsRunner("provisioning a stack")
}

func (AbsentEngine) PlanDestroy(context.Context, StackRef, progress.Log) (Plan, error) {
	return Plan{}, RefuseNeedsRunner("planning a stack's destruction")
}

func (AbsentEngine) Destroy(context.Context, StackDestroy, progress.Log) error {
	return RefuseNeedsRunner("destroying a stack")
}

func (AbsentEngine) Resources(context.Context, StackResources) ([]PhysicalResource, error) {
	return nil, RefuseNeedsRunner("reading a stack's resources")
}

func (AbsentEngine) Arch(context.Context, string, string) (string, error) {
	return "", RefuseNeedsRunner("choosing an app's architecture")
}

func (AbsentEngine) Binary(context.Context, string) ([]byte, error) {
	return nil, RefuseNeedsRunner("building the runtime binary")
}

func (AbsentEngine) FunctionBase(context.Context, buildoutput.Framework) (v1.Image, error) {
	return nil, RefuseNeedsRunner("building a function image")
}

func (AbsentEngine) FunctionFiles(context.Context, buildoutput.Framework) (map[string][]byte, error) {
	return nil, RefuseNeedsRunner("building a function image")
}

func (AbsentEngine) FunctionBuildPaths(buildoutput.Framework) []string { return nil }
