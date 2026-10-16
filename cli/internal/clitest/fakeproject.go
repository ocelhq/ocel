package clitest

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

type FakeProject struct {
	Root     string
	Provider *fake.Provider
	Requests *ProviderRequests
}

func SetUpProject(t *testing.T) FakeProject {
	t.Helper()

	root := writeProject(t)
	p := fake.NewForProject(fake.Options{}, root)
	Bootstrap(t, p, environment.TierProduction, fake.FeatureCache, fake.FeatureImages)
	return FakeProject{Root: root, Provider: p, Requests: ServeFake(t, p)}
}

func Bootstrap(t *testing.T, p *fake.Provider, tier environment.Tier, features ...string) {
	t.Helper()
	var others []string
	for _, offered := range p.FakeBootstrap().Catalogue() {
		if !slices.Contains(features, offered.Name) {
			others = append(others, offered.Name)
		}
	}
	if err := p.FakeBootstrap().Apply(context.Background(), provider.BootstrapRequest{Tier: tier, Features: features, Remove: others}, nil); err != nil {
		t.Fatalf("bootstrap the fake provider's %s tier: %v", tier, err)
	}
}

func SetUpMonorepoProject(t *testing.T, providerOptions string) FakeProject {
	t.Helper()

	project := SetUpProject(t)
	WriteUsageMonorepo(t, project.Root)
	writeEdgeConfig(t, project.Root, providerOptions)
	return project
}
