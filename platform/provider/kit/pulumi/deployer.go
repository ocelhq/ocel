package pulumi

import (
	"context"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	provider "github.com/ocelhq/ocel/platform/provider/contract"
)

type Outputs map[string]string

type Program func(ctx context.Context, spec provider.Spec, export func(key, value string)) error

type Engine interface {
	Preview(ctx context.Context, stack string, program Program, spec provider.Spec) ([]provider.Change, error)
	Up(ctx context.Context, stack string, program Program, spec provider.Spec, say func(string)) (Outputs, error)
	Outputs(ctx context.Context, stack string) (Outputs, error)
	Destroy(ctx context.Context, stack string, say func(string)) error
}

type Deployer struct {
	Engine    Engine
	Program   Program
	StackName func(spec provider.Spec) string
	Place     func(ctx context.Context, spec provider.Spec, progress provider.Progress) error
	Read      func(spec provider.Spec, outputs Outputs) Realized
}

type Realized struct {
	Resolver edge.Resolver
	Links    []provider.Link
	Records  []edge.DeploymentRecord
}

type state struct {
	Stack string        `json:"stack"`
	Spec  provider.Spec `json:"spec"`
}

func (d Deployer) Plan(ctx context.Context, spec provider.Spec, _ provider.State) (provider.Plan, error) {
	changes, err := d.Engine.Preview(ctx, d.StackName(spec), d.Program, spec)
	if err != nil {
		return provider.Plan{}, err
	}
	return provider.Plan{Changes: changes}, nil
}

func (d Deployer) Upload(ctx context.Context, spec provider.Spec, _ provider.Plan, progress provider.Progress) (provider.Uploaded, error) {
	if d.Place == nil {
		return provider.Uploaded{}, nil
	}
	return provider.Uploaded{}, d.Place(ctx, spec, progress)
}

func (d Deployer) Apply(ctx context.Context, spec provider.Spec, _ provider.Plan, _ provider.Uploaded, _ provider.State, progress provider.Progress) (provider.Deployment, error) {
	name := d.StackName(spec)
	outputs, err := d.Engine.Up(ctx, name, d.Program, spec, func(line string) { progress.Say("provision", line) })
	if err != nil {
		return nil, err
	}
	return &deployment{d: d, state: provider.State{Slug: spec.Slug, Class: spec.Class, Adapter: provider.Own(state{Stack: name, Spec: spec})}, realized: d.Read(spec, outputs)}, nil
}

func (d Deployer) Open(st provider.State) (provider.Deployment, error) {
	var own state
	if err := st.Adapter.Into(&own); err != nil {
		return nil, err
	}
	outputs, err := d.Engine.Outputs(context.Background(), own.Stack)
	if err != nil {
		return nil, err
	}
	return &deployment{d: d, state: st, realized: d.Read(own.Spec, outputs)}, nil
}

type deployment struct {
	d        Deployer
	state    provider.State
	realized Realized
}

func (x *deployment) State() provider.State            { return x.state }
func (x *deployment) Resolver() edge.Resolver          { return x.realized.Resolver }
func (x *deployment) Links() []provider.Link           { return x.realized.Links }
func (x *deployment) Records() []edge.DeploymentRecord { return x.realized.Records }
func (x *deployment) Destroy(ctx context.Context, p provider.Progress) error {
	var own state
	if err := x.state.Adapter.Into(&own); err != nil {
		return err
	}
	return x.d.Engine.Destroy(ctx, own.Stack, func(line string) { p.Say("destroy", line) })
}
