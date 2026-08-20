package fake

import (
	"context"
	"fmt"

	origin "github.com/ocelhq/ocel/platform/origin/contract"
)

type Origin struct {
	facts   origin.Facts
	native  []origin.Backing
	records *Records
}

func NewOrigin(facts origin.Facts, native ...origin.Backing) *Origin {
	return &Origin{facts: facts, native: native, records: &Records{}}
}

func (o *Origin) Facts() origin.Facts         { return o.facts }
func (o *Origin) Native() []origin.Backing    { return o.native }
func (o *Origin) Records() origin.RecordStore { return o.records }

func (o *Origin) Bootstrap(_ context.Context, class origin.Class) (origin.Substrate, error) {
	return origin.Substrate{Identity: origin.Identity{Origin: o.facts.Kind, Principal: fmt.Sprintf("%s:%s", o.facts.Identity, class)}}, nil
}

func (o *Origin) Teardown(context.Context, origin.Class) error { return nil }

func (o *Origin) Reconcile(_ context.Context, spec origin.DeploySpec, prior origin.State) (origin.Deployment, error) {
	env := map[string]string{}
	for _, l := range spec.Links {
		for k, v := range l.Values {
			env[k] = v
		}
		for k := range l.Secrets {
			env[k] = "<sealed>"
		}
	}
	return &deployment{state: origin.State{Slug: spec.Slug, Class: spec.Class, Identity: origin.Identity{Origin: o.facts.Kind, Principal: o.facts.Identity}, Adapter: origin.Own(env)}}, nil
}

func (o *Origin) Open(state origin.State) (origin.Deployment, error) {
	return &deployment{state: state}, nil
}

type deployment struct{ state origin.State }

func (d *deployment) State() origin.State       { return d.state }
func (d *deployment) Identity() origin.Identity { return d.state.Identity }
func (d *deployment) FunctionURL(route string) (string, error) {
	return "https://" + route + ".example", nil
}
func (d *deployment) Promote(context.Context, string) error { return nil }
func (d *deployment) Destroy(context.Context) error         { return nil }

type Records struct{ held map[string]origin.Record }

func key(slug string, class origin.Class) string { return slug + "/" + string(class) }

func (r *Records) Read(_ context.Context, slug string, class origin.Class) (origin.Record, bool, error) {
	rec, ok := r.held[key(slug, class)]
	return rec, ok, nil
}

func (r *Records) Write(_ context.Context, slug string, class origin.Class, rec origin.Record) error {
	if r.held == nil {
		r.held = map[string]origin.Record{}
	}
	r.held[key(slug, class)] = rec
	return nil
}

func (r *Records) Delete(_ context.Context, slug string, class origin.Class) error {
	delete(r.held, key(slug, class))
	return nil
}

type Backing struct{ facts origin.BackingFacts }

func NewBacking(facts origin.BackingFacts) *Backing { return &Backing{facts: facts} }

func (b *Backing) Facts() origin.BackingFacts { return b.facts }

func (b *Backing) Reconcile(_ context.Context, spec origin.ResourceSpec, _ origin.ResourceState) (origin.Resource, error) {
	return &resource{facts: b.facts, state: origin.ResourceState{Name: spec.Name, Kind: b.facts.Kind, Endpoint: fmt.Sprintf("%s://%s.%s", b.facts.Protocol, spec.Name, b.facts.Kind)}}, nil
}

func (b *Backing) Open(state origin.ResourceState) (origin.Resource, error) {
	return &resource{facts: b.facts, state: state}, nil
}

type resource struct {
	facts origin.BackingFacts
	state origin.ResourceState
}

func (r *resource) State() origin.ResourceState { return r.state }

func (r *resource) Link(_ context.Context, to origin.Identity) (origin.Link, error) {
	l := origin.Link{Role: r.facts.Role, Name: r.state.Name, Protocol: r.facts.Protocol, Endpoint: r.state.Endpoint, Values: map[string]string{}, Secrets: map[string]string{}}
	prefix := fmt.Sprintf("OCEL_%s_%s", r.facts.Role, r.state.Name)
	l.Values[prefix+"_ENDPOINT"] = r.state.Endpoint
	if r.facts.Native != "" && r.facts.Native == to.Origin {
		l.Granted = true
		return l, nil
	}
	for _, name := range r.facts.Brings {
		l.Secrets[prefix+"_"+name] = "***"
	}
	return l, nil
}

func (r *resource) Destroy(context.Context) error { return nil }
