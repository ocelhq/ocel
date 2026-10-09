package provider

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
)

type Lease struct {
	Tier    environment.Tier
	Project string
	Env     string
}

func (l Lease) Covers(ref StackRef) bool {
	return l.Project != "" && l.Tier == ref.Tier && l.Project == ref.Project && (l.Env == "" || l.Env == ref.Name.Env)
}

type leasesKey struct{}

func WithLease(ctx context.Context, lease Lease) context.Context {
	held, _ := ctx.Value(leasesKey{}).([]Lease)
	return context.WithValue(ctx, leasesKey{}, append(slices.Clip(held), lease))
}

func HasLease(ctx context.Context, ref StackRef) bool {
	if ctx.Err() != nil {
		return false
	}
	held, _ := ctx.Value(leasesKey{}).([]Lease)
	return slices.ContainsFunc(held, func(lease Lease) bool { return lease.Covers(ref) })
}
