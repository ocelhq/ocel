package deploy

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	kitpulumi "github.com/ocelhq/ocel/pkg/providerkit/pulumi"
)

func fixed(cfg Config) Resolver {
	return ResolverFunc(

		func(context.Context, Scope) (Config, error) { return cfg, nil })
}

func releasing(t *testing.T, cfg Config) *release {
	t.Helper()
	return releasingOn(t, cfg, nil)
}

func releasingOn(t *testing.T, cfg Config, engine kitpulumi.Engine) *release {
	t.Helper()
	held, err := newReleaser(fixed(cfg), &Realized{}, engine).at(context.Background(), providerkit.StackRef{})
	if err != nil {
		t.Fatalf("open a release: %v", err)
	}
	return held
}
