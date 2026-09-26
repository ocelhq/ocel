package bootstrapplan_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestPrefixWithVendorNamesEveryGroupUnderTheVendorThatOwnsIt(t *testing.T) {
	t.Parallel()

	groups := []provider.ChangeGroup{{Kind: provider.StackGroupKind, Name: "ocel-bootstrap"}}
	named := bootstrapplan.PrefixWithVendor("aws", groups)
	if named[0].Name != "aws/ocel-bootstrap" {
		t.Errorf("group name = %q, want the stack under its vendor", named[0].Name)
	}
	if groups[0].Name != "ocel-bootstrap" {
		t.Error("PrefixWithVendor() renamed the groups it was handed rather than returning named ones")
	}
}

func TestFeatureNeedingEdgeFindsTheFeatureTheEdgeParticipatesThrough(t *testing.T) {
	t.Parallel()

	catalogue := []provider.Feature{
		{Name: "isr"},
		{Name: "cloudflare-edge", Needs: []string{provider.NeedsEdgePrefix + "cloudflare"}},
	}
	if got := bootstrapplan.FeatureNeedingEdge(catalogue, "cloudflare"); got != "cloudflare-edge" {
		t.Errorf("FeatureNeedingEdge(cloudflare) = %q, want the feature that names it", got)
	}
	if got := bootstrapplan.FeatureNeedingEdge(catalogue, "cloudfront"); got != "" {
		t.Errorf("FeatureNeedingEdge(cloudfront) = %q, want nothing where no feature names it", got)
	}
}

func TestChangeGroupsNamesTheStacksTheVendorDescribed(t *testing.T) {
	t.Parallel()

	described := provider.BootstrapDescription{
		Class:   edge.ClassPreview,
		Present: true,
		Stacks: []provider.BootstrapStack{
			{Name: "core", Present: true, Schema: provider.BootstrapSchema, DigestCurrent: true},
			{Name: "cache-stack", Feature: fake.FeatureCache, Present: true, Schema: provider.BootstrapSchema},
		},
	}
	groups := bootstrapplan.ChangeGroups(described, fake.NewBootstrap().Catalogue(), provider.BootstrapRequest{
		Class:    edge.ClassPreview,
		Features: []string{fake.FeatureCache},
		Remove:   []string{fake.FeatureImages},
	})
	if len(groups) != 3 {
		t.Fatalf("ChangeGroups() = %v, want the baseline, the kept feature and the dropped one", groups)
	}
	if groups[0].Name != "core" || groups[0].Action != provider.ActionKeep {
		t.Errorf("the baseline group = %+v, want core kept", groups[0])
	}
	if groups[1].Name != "cache-stack" || groups[1].Action != provider.ActionUpdate {
		t.Errorf("the cache group = %+v, want the stale stack updated", groups[1])
	}
	if groups[2].Action != provider.ActionDelete || groups[2].Feature != fake.FeatureImages {
		t.Errorf("the images group = %+v, want the dropped feature deleted", groups[2])
	}
}
