package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

type stubBootstrapper struct{ err error }

func (stubBootstrapper) Catalogue() []providerkit.Feature { return nil }

func (stubBootstrapper) Describe(context.Context, providerkit.Class) (providerkit.Bootstrap, error) {
	return providerkit.Bootstrap{}, nil
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
	held, ok := p.Bootstrap().(settling)
	if !ok {
		t.Fatalf("Bootstrap() = %T, want one that settles the provider's memo", p.Bootstrap())
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
	if _, err := p.params.resolve(providerkit.ClassProduction, func() (bootstrap.ClassParams, error) {
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
	params, err := p.params.resolve(providerkit.ClassProduction, func() (bootstrap.ClassParams, error) {
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
