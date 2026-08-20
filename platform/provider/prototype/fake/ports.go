package fake

import (
	"context"
	"fmt"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	provider "github.com/ocelhq/ocel/platform/provider/contract"
)

type Substrate struct {
	Name string
	have map[edge.Class]provider.Bootstrapped
}

func (s *Substrate) Bootstrap(_ context.Context, class edge.Class, features []string, p provider.Progress) (provider.Bootstrapped, error) {
	if s.have == nil {
		s.have = map[edge.Class]provider.Bootstrapped{}
	}
	p.Say("bootstrap", fmt.Sprintf("%s: substrate for %s with %v", s.Name, class, features))
	b := provider.Bootstrapped{Features: features, Values: map[string]string{"substrate": s.Name}, Trust: edge.TrustInternal}
	s.have[class] = b
	return b, nil
}

func (s *Substrate) Describe(_ context.Context, class edge.Class) (provider.Bootstrapped, bool, error) {
	b, ok := s.have[class]
	return b, ok, nil
}

func (s *Substrate) PlanTeardown(context.Context, edge.Class) ([]edge.Surface, error) {
	return []edge.Surface{{Kind: "substrate", Name: s.Name, Action: edge.SurfaceDelete, Reason: "account-level state"}}, nil
}

func (s *Substrate) Teardown(_ context.Context, class edge.Class, p provider.Progress) error {
	delete(s.have, class)
	p.Say("teardown", s.Name+": substrate removed")
	return nil
}

type Records struct{ held map[string]provider.Record }

func key(slug string, class edge.Class) string { return slug + "/" + string(class) }

func (r *Records) Read(_ context.Context, slug string, class edge.Class) (provider.Record, bool, error) {
	rec, ok := r.held[key(slug, class)]
	return rec, ok, nil
}

func (r *Records) Write(_ context.Context, slug string, class edge.Class, rec provider.Record) error {
	if r.held == nil {
		r.held = map[string]provider.Record{}
	}
	r.held[key(slug, class)] = rec
	return nil
}

func (r *Records) Delete(_ context.Context, slug string, class edge.Class) error {
	delete(r.held, key(slug, class))
	return nil
}

func (r *Records) Slugs(_ context.Context, class edge.Class) ([]string, error) {
	var out []string
	for k, rec := range r.held {
		if rec.Deploy.Class == class {
			out = append(out, k)
		}
	}
	return out, nil
}

type Vars struct {
	vars  map[string]provider.Variable
	links map[string]provider.Link
}

func scopeKey(s provider.VarScope) string {
	return fmt.Sprintf("%s/%s/%s/%s", s.Slug, s.Class, s.Environment, s.App)
}

func (v *Vars) Set(_ context.Context, scope provider.VarScope, key, value string, secret bool) (int64, error) {
	if v.vars == nil {
		v.vars = map[string]provider.Variable{}
	}
	v.vars[scopeKey(scope)+"#"+key] = provider.Variable{Key: key, Value: value, Secret: secret, Version: 1}
	return 1, nil
}

func (v *Vars) Get(_ context.Context, scope provider.VarScope, key string) (provider.Variable, bool, error) {
	x, ok := v.vars[scopeKey(scope)+"#"+key]
	return x, ok, nil
}

func (v *Vars) List(_ context.Context, scope provider.VarScope) ([]provider.Variable, error) {
	var out []provider.Variable
	for k, x := range v.vars {
		if len(k) > len(scopeKey(scope)) && k[:len(scopeKey(scope))] == scopeKey(scope) {
			out = append(out, x)
		}
	}
	return out, nil
}

func (v *Vars) Delete(_ context.Context, scope provider.VarScope, key string) error {
	delete(v.vars, scopeKey(scope)+"#"+key)
	return nil
}

func (v *Vars) SetLink(_ context.Context, scope provider.VarScope, resource string, link provider.Link) error {
	if v.links == nil {
		v.links = map[string]provider.Link{}
	}
	v.links[scopeKey(scope)+"#"+resource] = link
	return nil
}

func (v *Vars) RemoveLink(_ context.Context, scope provider.VarScope, resource string) error {
	delete(v.links, scopeKey(scope)+"#"+resource)
	return nil
}

func (v *Vars) Links(_ context.Context, scope provider.VarScope) ([]provider.Link, error) {
	var out []provider.Link
	for k, l := range v.links {
		if len(k) > len(scopeKey(scope)) && k[:len(scopeKey(scope))] == scopeKey(scope) {
			out = append(out, l)
		}
	}
	return out, nil
}

type Edges struct {
	Default edge.Kind
	Known   map[edge.Kind]edge.Edge
}

func (e Edges) For(_ context.Context, kind edge.Kind) (edge.Edge, error) {
	if kind == "" {
		kind = e.Default
	}
	x, ok := e.Known[kind]
	if !ok {
		return nil, fmt.Errorf("no %q edge here; have %v", kind, e.Kinds())
	}
	return x, nil
}

func (e Edges) Kinds() []edge.Kind {
	out := make([]edge.Kind, 0, len(e.Known))
	for k := range e.Known {
		out = append(out, k)
	}
	return out
}

type DNS struct{}

func (DNS) WriterFor(context.Context, string, string) (edge.DNSWriter, error) { return nil, nil }
func (DNS) Kinds() []string                                                   { return nil }

type Printer struct{ Prefix string }

func (p Printer) Stage(id provider.StageID, title string, parent provider.StageID) {
	if parent == "" {
		fmt.Printf("%s  ▸ %-22s %s\n", p.Prefix, id, title)
	} else {
		fmt.Printf("%s    ▸ %-20s %s\n", p.Prefix, id, title)
	}
}
func (p Printer) Start(id provider.StageID) { fmt.Printf("%s  … %s\n", p.Prefix, id) }
func (p Printer) Done(id provider.StageID)  { fmt.Printf("%s  ✓ %s\n", p.Prefix, id) }
func (p Printer) Fail(id provider.StageID, err error) {
	fmt.Printf("%s  ✕ %s: %v\n", p.Prefix, id, err)
}
func (p Printer) Say(id provider.StageID, line string) {
	fmt.Printf("%s      %s: %s\n", p.Prefix, id, line)
}
func (p Printer) Count(id provider.StageID, cur, total uint32) {
	fmt.Printf("%s      %s: %d/%d\n", p.Prefix, id, cur, total)
}
