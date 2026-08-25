package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/control"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
)

type stubBootstrapper struct{ err error }

func (stubBootstrapper) Catalogue() []providerkit.Feature { return nil }

func (stubBootstrapper) Describe(context.Context, providerkit.Class) (providerkit.Bootstrap, error) {
	return providerkit.Bootstrap{}, nil
}

func (s stubBootstrapper) Plan(context.Context, providerkit.BootstrapRequest) (providerkit.BootstrapPlan, error) {
	return providerkit.BootstrapPlan{}, s.err
}

func (s stubBootstrapper) Apply(context.Context, providerkit.BootstrapRequest, providerkit.Reporter) error {
	return s.err
}

func (stubBootstrapper) Removals(context.Context, providerkit.Class) ([]providerkit.Removal, error) {
	return nil, nil
}

func (s stubBootstrapper) Remove(context.Context, providerkit.Class, providerkit.Reporter) error {
	return s.err
}

func settledBy(t *testing.T, p *Provider) func() {
	t.Helper()
	bootstrapper, err := p.Bootstrap(edges.DefaultKind)
	if err != nil {
		t.Fatal(err)
	}
	held, ok := bootstrapper.(settling)
	if !ok {
		t.Fatalf("Bootstrap() = %T, want one that settles the provider's memo", bootstrapper)
	}
	return held.settled
}

func primed(t *testing.T, p *Provider, table string) {
	t.Helper()
	if _, err := p.deployed.resolve(providerkit.ClassProduction, func() (bootstrap.Deployed, error) {
		return bootstrap.Deployed{StateTable: table}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.params.resolve(classEdge{class: providerkit.ClassProduction, kind: edges.DefaultKind}, func() (bootstrap.ClassParams, error) {
		return bootstrap.ClassParams{Passphrase: table}, nil
	}); err != nil {
		t.Fatal(err)
	}
}

func standing(t *testing.T, p *Provider) (string, string) {
	t.Helper()
	held, err := p.deployed.resolve(providerkit.ClassProduction, func() (bootstrap.Deployed, error) {
		return bootstrap.Deployed{StateTable: "after"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	params, err := p.params.resolve(classEdge{class: providerkit.ClassProduction, kind: edges.DefaultKind}, func() (bootstrap.ClassParams, error) {
		return bootstrap.ClassParams{Passphrase: "after"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return held.StateTable, params.Passphrase
}

func TestBootstrapApplyForgetsWhatItStoodUp(t *testing.T) {
	p := NewProvider(Options{}, aws.Config{})
	primed(t, p, "before")

	if err := (settling{Bootstrapper: stubBootstrapper{}, settled: settledBy(t, p)}).
		Apply(context.Background(), providerkit.BootstrapRequest{Class: providerkit.ClassProduction}, nil); err != nil {
		t.Fatal(err)
	}

	table, passphrase := standing(t, p)
	if table != "after" || passphrase != "after" {
		t.Fatalf("after Apply the provider still holds %q/%q, want the account read afresh", table, passphrase)
	}
}

func TestBootstrapRemoveForgetsWhatItTookDown(t *testing.T) {
	p := NewProvider(Options{}, aws.Config{})
	primed(t, p, "before")

	if err := (settling{Bootstrapper: stubBootstrapper{}, settled: settledBy(t, p)}).
		Remove(context.Background(), providerkit.ClassProduction, nil); err != nil {
		t.Fatal(err)
	}

	table, passphrase := standing(t, p)
	if table != "after" || passphrase != "after" {
		t.Fatalf("after Remove the provider still holds %q/%q, want the account read afresh", table, passphrase)
	}
}

func TestBootstrapKeepsWhatAFailedApplyNeverChanged(t *testing.T) {
	p := NewProvider(Options{}, aws.Config{})
	primed(t, p, "before")

	refused := errors.New("refused")
	if err := (settling{Bootstrapper: stubBootstrapper{err: refused}, settled: settledBy(t, p)}).
		Apply(context.Background(), providerkit.BootstrapRequest{Class: providerkit.ClassProduction}, nil); !errors.Is(err, refused) {
		t.Fatalf("Apply() = %v, want the refusal it was given", err)
	}

	table, passphrase := standing(t, p)
	if table != "before" || passphrase != "before" {
		t.Fatalf("a failed Apply forgot %q/%q, want what the account still holds", table, passphrase)
	}
}

func TestBootstrapFrontsTheEdgeItWasAsked(t *testing.T) {
	p := NewProvider(Options{}, aws.Config{})
	for _, kind := range edges.SupportedEdges() {
		bootstrapper, err := p.Bootstrap(kind)
		if err != nil {
			t.Fatalf("Bootstrap(%q) error = %v", kind, err)
		}
		held, ok := bootstrapper.(settling).Bootstrapper.(control.Bootstrapper)
		if !ok {
			t.Fatalf("Bootstrap(%q) = %T, want the AWS bootstrapper", kind, bootstrapper)
		}
		if held.Edge.Kind() != kind {
			t.Errorf("Bootstrap(%q) fronts the %q edge, want the one it was asked for", kind, held.Edge.Kind())
		}
	}
	if _, err := p.Bootstrap("nowhere"); err == nil {
		t.Error("Bootstrap() accepted an edge this provider cannot front with")
	}
}
