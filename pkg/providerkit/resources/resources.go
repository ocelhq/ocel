// Package resources turns a set of per-primitive functions into a Releaser.
//
// It exists because the two things a vendor wants from provisioning pull in
// opposite directions. The engine wants a whole graph, so it can order it, diff
// it and delete what fell out of it. The author wants one function per primitive,
// so they can write a bucket without thinking about a database — and so someone
// else can replace exactly one of them.
//
// So the port stays whole-stack and this package is the authoring shape over
// it. Each primitive is its own one-method interface, found by assertion, which
// is what makes an existing provider extensible: embed it, define the one method
// you want to do differently, and the assertion finds yours.
//
//	type NeonOnCloudflare struct{ cloudflare.Resources }
//
//	func (NeonOnCloudflare) Postgres(ctx context.Context, at Scope, name string, spec providerkit.PostgresSpec, report providerkit.Reporter) (providerkit.Link, error) {
//		// ...call Neon, return the connection properties
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
	Stack providerkit.StackRef
	Tags  map[string]string
}

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
	Postgres(ctx context.Context, at Scope, name string, spec providerkit.PostgresSpec, report providerkit.Reporter) (providerkit.Link, error)
}

type Bucket interface {
	Bucket(ctx context.Context, at Scope, name string, spec providerkit.BucketSpec, report providerkit.Reporter) (providerkit.Link, error)
}

// Function is the app-stack primitive: one per FunctionSpec, with the app's
// grants alongside because a function's reach is decided per app, not per
// function.
type Function interface {
	Function(ctx context.Context, at Scope, spec providerkit.FunctionSpec, grants []providerkit.Link, report providerkit.Reporter) (providerkit.Function, error)
}

// Container is here before containers are, because the shape has to survive them.
// It is the primitive that most argues against making per-primitive the *port*:
// one container becomes a cluster, a task definition, a target group, a service
// and a role, in that order, on one vendor — and one `docker run` on another.
type Container interface {
	Container(ctx context.Context, at Scope, name string, spec providerkit.ContainerSpec, report providerkit.Reporter) (providerkit.Link, error)
}

// Custom is the escape hatch the wire already has: a resource whose properties
// are an opaque struct, for a vendor offering something the enum has no word for.
type Custom interface {
	Custom(ctx context.Context, at Scope, name string, spec providerkit.CustomSpec, report providerkit.Reporter) (providerkit.Link, error)
}

// Remover deletes by identity rather than by shape, which is why it is one method
// and not one per primitive. Destroy walks the stack's refs through it.
type Remover interface {
	Remove(ctx context.Context, at Scope, ref Ref, report providerkit.Reporter) error
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

// Releaser wraps per-primitive functions as the whole-stack port. The kit still
// hands it one stack, it still walks that stack in the kit's order, and the
// vendor still never implements ordering.
//
// What it cannot do is give a non-engine provider the engine's whole-graph diff:
// a resource that fell out of the manifest is found by the kit comparing the plan
// against its own records, and removed through Remover. A provider composing a
// graph engine keeps the engine's own diff instead, and should implement
// providerkit.Releaser directly.
func Releaser(impl any) providerkit.Releaser { return fanOut{impl: impl} }

type fanOut struct{ impl any }

func (f fanOut) Provision(ctx context.Context, plan providerkit.StackPlan, report providerkit.Reporter) (providerkit.StackResult, error) {
	at := Scope{Stack: plan.Ref, Tags: plan.Tags}
	var result providerkit.StackResult
	for _, resource := range plan.Resources {
		link, err := f.one(ctx, at, resource, report)
		if err != nil {
			return result, err
		}
		result.Links = append(result.Links, link)
	}
	if plan.App != nil {
		impl, ok := f.impl.(Function)
		if !ok {
			return result, providerkit.Refuse(providerkit.CodeInvalid,
				"this provider cannot run a function, so %q has nowhere to go; it makes %s",
				plan.App.App, join(Serves(f.impl)))
		}
		for _, spec := range plan.App.Functions {
			fn, err := impl.Function(ctx, at, spec, plan.App.Grants, report)
			if err != nil {
				return result, err
			}
			result.Functions = append(result.Functions, fn)
		}
	}
	return result, nil
}

func (f fanOut) one(ctx context.Context, at Scope, resource providerkit.Resource, report providerkit.Reporter) (providerkit.Link, error) {
	switch resource.Type {
	case "postgres":
		if impl, ok := f.impl.(Postgres); ok && resource.Postgres != nil {
			return impl.Postgres(ctx, at, resource.Name, *resource.Postgres, report)
		}
	case "bucket":
		if impl, ok := f.impl.(Bucket); ok && resource.Bucket != nil {
			return impl.Bucket(ctx, at, resource.Name, *resource.Bucket, report)
		}
	case "container":
		if impl, ok := f.impl.(Container); ok && resource.Container != nil {
			return impl.Container(ctx, at, resource.Name, *resource.Container, report)
		}
	default:
		if impl, ok := f.impl.(Custom); ok && resource.Custom != nil {
			return impl.Custom(ctx, at, resource.Name, *resource.Custom, report)
		}
	}
	return providerkit.Link{}, f.unserved(resource)
}

func (f fanOut) unserved(resource providerkit.Resource) error {
	return providerkit.Refuse(providerkit.CodeInvalid,
		"this provider cannot make a %s, so %q has nowhere to go; it makes %s",
		resource.Type, resource.Name, join(Serves(f.impl)))
}

// Destroy removes what the kit's records say the stack holds. The refs are not
// on the StackRef, so this is the one place the fan-out must be told more than
// the port signature carries; providerkit/resources resolves that by reading the
// kit's records for the stack, which is the same source the kit diffs against.
//
// TODO(#508): the records read is stubbed until RecordStore's record shapes are
// fixed; Remover is asserted now so a provider that cannot remove is refused
// before anything is half torn down.
func (f fanOut) Destroy(ctx context.Context, ref providerkit.StackRef, report providerkit.Reporter) error {
	remover, ok := f.impl.(Remover)
	if !ok {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"this provider can make resources but not remove them, so %s cannot be torn down", ref.Name)
	}
	_ = remover
	return nil
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
