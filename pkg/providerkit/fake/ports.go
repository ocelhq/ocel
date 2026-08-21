package fake

import (
	"context"
	"slices"
	"sync"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Bootstrapper struct {
	mu      sync.Mutex
	applied map[providerkit.Class][]string
}

func NewBootstrapper() *Bootstrapper {
	return &Bootstrapper{applied: map[providerkit.Class][]string{}}
}

func (b *Bootstrapper) Catalogue() []providerkit.Feature {
	return []providerkit.Feature{{Name: "core", Summary: "the reference provider's only feature"}}
}

func (b *Bootstrapper) Describe(_ context.Context, class providerkit.Class) (providerkit.Bootstrap, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	features, present := b.applied[class]
	described := providerkit.Bootstrap{Class: class, Present: present}
	for _, feature := range features {
		described.Stacks = append(described.Stacks, providerkit.BootstrapStack{
			Name:          feature,
			Feature:       feature,
			Present:       true,
			DigestCurrent: true,
			Writer:        string(Vendor),
		})
	}
	return described, nil
}

func (b *Bootstrapper) Apply(_ context.Context, req providerkit.BootstrapRequest, report providerkit.Reporter) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.applied[req.Class] = slices.Clone(req.Features)
	if report != nil {
		report.Say("bootstrapped " + string(req.Class))
	}
	return nil
}

func (b *Bootstrapper) Removals(_ context.Context, class providerkit.Class) ([]providerkit.Removal, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, present := b.applied[class]; !present {
		return nil, nil
	}
	return []providerkit.Removal{{
		Kind:   "bootstrap",
		Name:   string(class),
		Action: edge.SurfaceDelete,
	}}, nil
}

func (b *Bootstrapper) Remove(_ context.Context, class providerkit.Class, report providerkit.Reporter) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.applied, class)
	if report != nil {
		report.Say("removed " + string(class))
	}
	return nil
}

type Releaser struct {
	mu     sync.Mutex
	stacks map[string]providerkit.StackResult
}

func NewReleaser() *Releaser {
	return &Releaser{stacks: map[string]providerkit.StackResult{}}
}

func (r *Releaser) Provision(_ context.Context, plan providerkit.StackPlan, report providerkit.Reporter) (providerkit.StackResult, error) {
	result := providerkit.StackResult{}
	for _, resource := range plan.Resources {
		result.Links = append(result.Links, providerkit.Link{
			Type:       resource.Type,
			Name:       resource.Name,
			Properties: propertiesFor(resource.Type),
		})
	}
	if plan.App != nil {
		for _, function := range plan.App.Functions {
			result.Functions = append(result.Functions, providerkit.Function{
				Name:     function.Name,
				Physical: plan.Ref.Name.String() + "/" + function.Name,
			})
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stacks[stackKey(plan.Ref)] = result
	if report != nil {
		report.Say("provisioned " + plan.Ref.Name.String())
	}
	return result, nil
}

func (r *Releaser) Destroy(_ context.Context, ref providerkit.StackRef, report providerkit.Reporter) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.stacks, stackKey(ref))
	if report != nil {
		report.Say("destroyed " + ref.Name.String())
	}
	return nil
}

func (r *Releaser) State(ref providerkit.StackRef) providerkit.StackState {
	r.mu.Lock()
	defer r.mu.Unlock()
	result, present := r.stacks[stackKey(ref)]
	return providerkit.StackState{Present: present, Result: result}
}

func stackKey(ref providerkit.StackRef) string {
	return ref.Project + "/" + string(ref.Class) + "/" + ref.Name.String()
}

func propertiesFor(t providerkit.LinkType) map[string]string {
	properties := map[string]string{}
	for _, name := range providerkit.RequiredProperties(t) {
		properties[name] = "fake"
	}
	return properties
}

type credentials struct{}

func (credentials) Whoami(context.Context) (providerkit.Identity, error) {
	return providerkit.Identity{
		Provider:  Vendor,
		Account:   "000000000000",
		Principal: "fake/reference",
	}, nil
}

func (credentials) Policy(tier providerkit.CredentialTier) (string, error) {
	return "fake policy for " + string(tier), nil
}

type edges struct{}

func (edges) Supported() []edge.Kind { return nil }

func (edges) Default() edge.Kind { return "" }

func (edges) Open(kind edge.Kind) (edge.Edge, error) {
	return nil, providerkit.Refuse(providerkit.CodeInvalid, "the reference provider serves no edge %q", kind)
}

type dns struct{}

func (dns) Supported() []providerkit.DNSKind { return nil }

func (dns) Default() providerkit.DNSKind { return "" }

func (dns) Open(kind providerkit.DNSKind, _ string) (edge.DNSWriter, error) {
	return nil, providerkit.Refuse(providerkit.CodeInvalid, "the reference provider writes no dns %q", kind)
}
