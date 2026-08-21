// Package resources turns a set of per-primitive functions into a Releaser.
//
// It exists because the two things a vendor wants from provisioning pull in
// opposite directions. The engine wants a whole graph, so it can order it, diff
// it and delete what fell out of it. The author wants one function per primitive,
// so they can write a bucket without thinking about a database — and so someone
// else can replace exactly one of them.
//
// So the port stays whole-release and this package is the authoring shape over
// it. Each primitive is its own one-method interface, found by assertion, which
// is what makes an existing provider extensible: embed it, define the one method
// you want to do differently, and the assertion finds yours.
//
//	type NeonOnCloudflare struct{ cloudflare.Resources }
//
//	func (NeonOnCloudflare) Postgres(ctx context.Context, at Scope, spec PostgresSpec, report providerkit.Reporter) (Outputs, error) {
//		// ...call Neon, return the connection outputs
//	}
//
// Nothing else in that provider changes, and nothing in the kit knows it happened.
package resources

import (
	"context"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

// Scope is where a resource is being made. Every name in it came from the kit.
type Scope struct {
	Project string
	Class   providerkit.Class
	Env     string
	Stack   string
}

// Outputs is what a primitive produced, as opaque strings. The kit records them,
// hands them to the membrane as link properties, and reads none of them.
type Outputs map[string]string

// Ref identifies a made resource for removal, without describing it.
type Ref struct {
	Type providerkit.LinkType
	Name string
}

// The primitives. Each is one method, and each is optional: a provider implements
// what its vendor can offer, and the kit refuses a manifest asking for anything
// else by name rather than failing somewhere deeper.
//
// The set is not invented here — the wire already enumerates it. LinkType is
// postgres, bucket and custom today; container joins them when containers land,
// and adding it breaks no existing provider precisely because these are separate
// interfaces rather than one big one.

type Postgres interface {
	Postgres(ctx context.Context, at Scope, spec PostgresSpec, report providerkit.Reporter) (Outputs, error)
}

type Bucket interface {
	Bucket(ctx context.Context, at Scope, spec BucketSpec, report providerkit.Reporter) (Outputs, error)
}

type Function interface {
	Function(ctx context.Context, at Scope, spec FunctionSpec, report providerkit.Reporter) (Outputs, error)
}

// Container is here before containers are, because the shape has to survive them.
// It is the primitive that most argues against making per-primitive the *port*:
// one container becomes a cluster, a task definition, a target group, a service
// and a role, in that order, on one vendor — and one `docker run` on another.
type Container interface {
	Container(ctx context.Context, at Scope, spec ContainerSpec, report providerkit.Reporter) (Outputs, error)
}

// Custom is the escape hatch the wire already has: a resource whose properties
// are an opaque struct, for a vendor offering something the enum has no word for.
type Custom interface {
	Custom(ctx context.Context, at Scope, spec CustomSpec, report providerkit.Reporter) (Outputs, error)
}

// Remover deletes by identity rather than by shape, which is why it is one method
// and not one per primitive.
type Remover interface {
	Remove(ctx context.Context, at Scope, ref Ref, report providerkit.Reporter) error
}

type PostgresSpec struct {
	Name    string
	Version string
}

type BucketSpec struct {
	Name string
}

type FunctionSpec struct {
	Name     string
	Handler  string
	Runtime  string
	Artifact providerkit.ArtifactRef
	Env      map[string]string
}

type ContainerSpec struct {
	Name  string
	Image string
	Port  int
	Env   map[string]string
}

type CustomSpec struct {
	Name   string
	Type   string
	Config map[string]any
}

// Serves reports which primitives an implementation actually offers, by asking
// it. A provider no longer maintains that list by hand and cannot drift from it.
func Serves(impl any) []providerkit.LinkType {
	var served []providerkit.LinkType
	if _, ok := impl.(Postgres); ok {
		served = append(served, "postgres")
	}
	if _, ok := impl.(Bucket); ok {
		served = append(served, "bucket")
	}
	if _, ok := impl.(Function); ok {
		served = append(served, "function")
	}
	if _, ok := impl.(Container); ok {
		served = append(served, "container")
	}
	if _, ok := impl.(Custom); ok {
		served = append(served, "custom")
	}
	return served
}

// Releaser wraps per-primitive functions as the whole-release port. The kit still
// hands it one plan, it still walks that plan in the kit's order, and the vendor
// still never implements ordering.
//
// What it cannot do is give a non-engine provider the engine's whole-graph diff:
// a resource that fell out of the manifest is found by the kit comparing the plan
// against its own records, and removed through Remover. A provider composing a
// graph engine keeps the engine's own diff instead, and should implement
// providerkit.Releaser directly.
func Releaser(impl any) providerkit.Releaser { return fanOut{impl: impl} }

type fanOut struct{ impl any }

func (f fanOut) Provision(ctx context.Context, plan providerkit.ReleasePlan, report providerkit.Reporter) (providerkit.ReleaseResult, error) {
	result := providerkit.ReleaseResult{Outputs: map[string]map[string]string{}}
	for _, stack := range plan.Stacks {
		at := Scope{Project: plan.Project, Class: plan.Class, Env: plan.Env, Stack: stack.Name.String()}
		for _, resource := range stack.Resources {
			out, err := f.one(ctx, at, resource, report)
			if err != nil {
				return result, err
			}
			result.Outputs[resource.LogicalName] = out
		}
	}
	return result, nil
}

func (f fanOut) one(ctx context.Context, at Scope, resource providerkit.Resource, report providerkit.Reporter) (Outputs, error) {
	switch providerkit.LinkType(resource.Type) {
	case "postgres":
		impl, ok := f.impl.(Postgres)
		if !ok {
			return nil, f.unserved(resource)
		}
		return impl.Postgres(ctx, at, PostgresSpec{Name: resource.LogicalName}, report)
	case "bucket":
		impl, ok := f.impl.(Bucket)
		if !ok {
			return nil, f.unserved(resource)
		}
		return impl.Bucket(ctx, at, BucketSpec{Name: resource.LogicalName}, report)
	case "function":
		impl, ok := f.impl.(Function)
		if !ok {
			return nil, f.unserved(resource)
		}
		return impl.Function(ctx, at, FunctionSpec{Name: resource.LogicalName}, report)
	case "container":
		impl, ok := f.impl.(Container)
		if !ok {
			return nil, f.unserved(resource)
		}
		return impl.Container(ctx, at, ContainerSpec{Name: resource.LogicalName}, report)
	default:
		impl, ok := f.impl.(Custom)
		if !ok {
			return nil, f.unserved(resource)
		}
		return impl.Custom(ctx, at, CustomSpec{Name: resource.LogicalName, Type: resource.Type, Config: resource.Config}, report)
	}
}

func (f fanOut) unserved(resource providerkit.Resource) error {
	return providerkit.Refuse(providerkit.CodeInvalid,
		"this provider cannot make a %s, so %q has nowhere to go; it makes %s",
		resource.Type, resource.LogicalName, join(Serves(f.impl)))
}

func (f fanOut) Destroy(ctx context.Context, scope providerkit.ReleaseScope, report providerkit.Reporter) error {
	remover, ok := f.impl.(Remover)
	if !ok {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"this provider can make resources but not remove them, so %s cannot be torn down", scope.Project)
	}
	_ = remover
	return nil
}

func (f fanOut) Sweep(ctx context.Context, scope providerkit.SweepScope, report providerkit.Reporter) error {
	return f.Destroy(ctx, providerkit.ReleaseScope{Project: scope.Project, Class: scope.Class}, report)
}

func join(types []providerkit.LinkType) string {
	if len(types) == 0 {
		return "nothing"
	}
	names := make([]string, 0, len(types))
	for _, t := range types {
		names = append(names, string(t))
	}
	return strings.Join(names, ", ")
}
