package component

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	ocelpulumi "github.com/ocelhq/ocel/pkg/provider/pulumi"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
)

type Component struct {
	Types []provider.BindingType
	InVPC bool

	Declare func(ctx *pulumi.Context, at Stack, declared []provider.Resource) error
	Collect func(ctx context.Context, at Stack, declared provider.Resource, outputs auto.OutputMap) (*variablestorev1.PublishedBinding, error)

	WidestGrants func(at Stack, declared provider.Resource) []*variablestorev1.Grant
	Credential   func(scope iam.Scope) []iam.Statement
	Sweep        func(ctx context.Context, at Stack, kept []provider.Resource) error
	Teardown     *Teardown
	Transform    func(at Stack, declared provider.Resource) (Transformable, error)
	Shape        func(cost CostTree, scope string, at Stack, declared []provider.Resource) error
}

type Teardown struct {
	Plan   func(plan provider.Plan, ref provider.StackRef) provider.Plan
	Before func(ctx context.Context, ref provider.StackRef, outputs func(context.Context) (auto.OutputMap, error), progress progress.Log) error
}

type Stack struct {
	Ref       provider.StackRef
	Namespace string
	Project   string
	Env       string
	Region    string

	Previewing bool

	StateTable     string
	StateTableARN  string
	AppBoundaryARN string
	AppRolePath    string

	Network    Network
	Transforms Transforms
	Resources  []provider.Resource
}

func (s Stack) Tier() environment.Tier { return s.Ref.Tier }

type Network struct {
	VPCID     string
	SubnetIDs []string
	Clients   string
}

type Transforms interface {
	TagsFor(kind, name string) map[string]string
	Patches(kind, name, surface string) bool
}

type Transformable struct {
	Kind  string
	Names map[string][]ocelpulumi.ResourceRef
	Check func(name string, patches map[string]map[string]any) error
}

type CostTree interface {
	Add(scope, typ, name string, properties map[string]any, ref ocelpulumi.ResourceRef)
	AddPlain(scope, typ, name string, properties map[string]any)
}

type Registry struct {
	components []Component
}

func NewRegistry(components ...Component) Registry {
	return Registry{components: components}
}

func (r Registry) Components() []Component { return slices.Clone(r.components) }

func (r Registry) Types() []provider.BindingType {
	var types []provider.BindingType
	for _, c := range r.components {
		types = append(types, c.Types...)
	}
	return types
}

func (r Registry) For(kind provider.BindingType) (Component, bool) {
	for _, c := range r.components {
		if slices.Contains(c.Types, kind) {
			return c, true
		}
	}
	return Component{}, false
}

func (r Registry) PlacesInVPC(resources []provider.Resource) []provider.Resource {
	var placed []provider.Resource
	for _, c := range r.components {
		if c.InVPC {
			placed = append(placed, c.Declared(resources)...)
		}
	}
	return placed
}

func (r Registry) RefuseUnserved(resources []provider.Resource) error {
	for _, resource := range resources {
		if resource.Binding != "" {
			continue
		}
		if _, serves := r.For(resource.Type); serves {
			continue
		}
		served := make([]string, 0, len(r.components))
		for _, kind := range r.Types() {
			served = append(served, string(kind))
		}
		return refusal.Refuse(refusal.CodeInvalid,
			"this provider provisions no %s; it provisions %s", resource.Type, listed(served))
	}
	return nil
}

func (c Component) Declared(resources []provider.Resource) []provider.Resource {
	var out []provider.Resource
	for _, resource := range resources {
		if resource.Binding == "" && slices.Contains(c.Types, resource.Type) {
			out = append(out, resource)
		}
	}
	return out
}

func listed(names []string) string {
	switch len(names) {
	case 0:
		return "nothing"
	case 1:
		return names[0]
	}
	return fmt.Sprintf("%s and %s", strings.Join(names[:len(names)-1], ", "), names[len(names)-1])
}
