package kit

import (
	"context"
	"errors"
	"fmt"

	origin "github.com/ocelhq/ocel/platform/origin/contract"
)

type Host struct {
	Origin      origin.Origin
	Independent []origin.Backing
	backings    map[origin.Role]map[origin.Kind]origin.Backing
}

func New(o origin.Origin, independent ...origin.Backing) *Host {
	h := &Host{Origin: o, Independent: independent, backings: map[origin.Role]map[origin.Kind]origin.Backing{}}
	for _, b := range append(o.Native(), independent...) {
		f := b.Facts()
		if h.backings[f.Role] == nil {
			h.backings[f.Role] = map[origin.Kind]origin.Backing{}
		}
		h.backings[f.Role][f.Kind] = b
	}
	return h
}

func (h *Host) Catalog() origin.Catalog { return origin.BuildCatalog(h.Origin, h.Independent...) }

func (h *Host) Preflight(t origin.Topology, req origin.Requirements) origin.Plan {
	return origin.Resolve(h.Catalog(), t, req)
}

type Result struct {
	Plan  origin.Plan
	Links []origin.Link
	State origin.State
	Log   []string
}

func (h *Host) Deploy(ctx context.Context, t origin.Topology, req origin.Requirements, spec origin.DeploySpec) (Result, error) {
	res := Result{Plan: h.Preflight(t, req)}
	if !res.Plan.OK() {
		errs := make([]error, len(res.Plan.Refusals))
		for i, r := range res.Plan.Refusals {
			errs[i] = r
		}
		return res, errors.Join(errs...)
	}

	substrate, err := h.Origin.Bootstrap(ctx, spec.Class)
	if err != nil {
		return res, err
	}
	prior, _, err := h.Origin.Records().Read(ctx, spec.Slug, spec.Class)
	if err != nil {
		return res, err
	}
	if prior.Origin.Resources == nil {
		prior.Origin.Resources = map[string]origin.ResourceState{}
	}

	for _, f := range res.Plan.Fills {
		if _, isResource := origin.ReachOf(f.Role); !isResource {
			continue
		}
		backing := h.backings[f.Role][f.Kind]
		r, err := backing.Reconcile(ctx, origin.ResourceSpec{Slug: spec.Slug, Class: spec.Class, Name: f.Name, Options: t.Resources[f.Role].Options}, prior.Origin.Resources[f.Name])
		if err != nil {
			return res, fmt.Errorf("%s %q via %s: %w", f.Role, f.Name, f.Kind, err)
		}
		link, err := r.Link(ctx, substrate.Identity)
		if err != nil {
			return res, err
		}
		res.Links = append(res.Links, link)
		prior.Origin.Resources[f.Name] = r.State()
		res.Log = append(res.Log, fmt.Sprintf("%s %q ← %s (%s, granted=%t)", f.Role, f.Name, f.Kind, f.Reach, link.Granted))
	}

	spec.Links = res.Links
	dep, err := h.Origin.Reconcile(ctx, spec, prior.Origin)
	if err != nil {
		return res, err
	}
	res.Log = append(res.Log, fmt.Sprintf("origin %s reconciled with %d links", h.Origin.Facts().Kind, len(res.Links)))

	if err := dep.Promote(ctx, "p1"); err != nil {
		return res, err
	}
	res.State = dep.State()
	res.State.Resources = prior.Origin.Resources
	prior.Origin = res.State
	return res, h.Origin.Records().Write(ctx, spec.Slug, spec.Class, prior)
}
