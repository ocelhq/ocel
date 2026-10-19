//go:build integration

package aws_test

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	aws "github.com/ocelhq/ocel/platform/aws/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
)

var unemulated = []provider.BindingType{
	provider.BindingRealtime,
	provider.BindingBucket,
	provider.BindingPostgres,
}

type emulatedFacts struct{ provider.Provider }

func (e emulatedFacts) Facts() provider.Facts {
	facts := e.Provider.Facts()
	facts.Bindings = slices.DeleteFunc(slices.Clone(facts.Bindings), func(b provider.BindingType) bool { return slices.Contains(unemulated, b) })
	return facts
}

func TestLiveAWSProviderConformsWithItsAccountBehindIt(t *testing.T) {
	a := live(t)
	ctx := context.Background()
	tiers := []environment.Tier{environment.TierProduction, environment.TierPreview}
	boot := a.emptied(t, tiers...)
	for _, tier := range tiers {
		if err := boot.Apply(ctx, provider.BootstrapRequest{Tier: tier, WrittenBy: liveWriter, Parts: []string{provider.VariablesKeyPart}}, nil); err != nil {
			t.Fatalf("Apply(%s) = %v", tier, err)
		}
	}
	p, err := aws.New(ctx, provider.Settings{WrittenBy: liveWriter, Options: provider.Options{"region": liveRegion}})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	if err := p.Hooks().Preflight.Apply(ctx, provider.DeployPreflight{
		Deploy:    provider.DeploySpec{Slug: "conformance", Tier: environment.TierPreview},
		Resources: []provider.Resource{{Name: "kv--c-kv", Declared: "c-kv", Type: provider.BindingKV}},
	}); err != nil {
		t.Fatalf("Preflight.Apply() of a deploy placing a store in the preview tier's VPC = %v", err)
	}

	conformance.Run(t, conformance.Suite{
		New: func(ctx context.Context, settings provider.Settings) (provider.Provider, error) {
			served, err := aws.New(ctx, settings)
			if err != nil || !a.emulated() {
				return served, err
			}
			return emulatedFacts{served}, nil
		},
		Server:       providerserver.Config{Version: "test", New: aws.New},
		Options:      provider.Options{"region": liveRegion},
		Binary:       buildProvider(t),
		Certificates: &conformance.CertificateChecks{Kind: edges.DefaultKind},
	})
}
