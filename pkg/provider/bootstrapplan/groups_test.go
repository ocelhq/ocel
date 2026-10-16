package bootstrapplan_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/provider/fake"
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

func TestPartNeedingEdgeFindsThePartTheEdgeParticipatesThrough(t *testing.T) {
	t.Parallel()

	offered := []provider.Part{
		{Name: "isr"},
		{Name: "cloudflare-edge", Edges: []edge.Kind{"cloudflare"}},
	}
	if got := bootstrapplan.PartFrontingEdge(offered, "cloudflare"); got != "cloudflare-edge" {
		t.Errorf("PartFrontingEdge(cloudflare) = %q, want the part that names it", got)
	}
	if got := bootstrapplan.PartFrontingEdge(offered, "cloudfront"); got != "" {
		t.Errorf("PartFrontingEdge(cloudfront) = %q, want nothing where no part names it", got)
	}
}

func TestChangeGroupsNamesTheStacksTheVendorDescribed(t *testing.T) {
	t.Parallel()

	described := provider.BootstrapDescription{
		Tier:    environment.TierPreview,
		Present: true,
		Stacks: []provider.BootstrapStack{
			{Name: "core", Present: true, DigestCurrent: true},
			{Name: "cache-stack", Part: fake.PartCache, Present: true},
		},
	}
	groups := bootstrapplan.ChangeGroups(described, fake.NewBootstrap().Parts(), provider.BootstrapRequest{
		Tier:   environment.TierPreview,
		Parts:  []string{fake.PartCache},
		Remove: []string{fake.PartImages},
	})
	if len(groups) != 3 {
		t.Fatalf("ChangeGroups() = %v, want the baseline, the kept part and the dropped one", groups)
	}
	if groups[0].Name != "core" || groups[0].Action != provider.ActionKeep {
		t.Errorf("the baseline group = %+v, want core kept", groups[0])
	}
	if groups[1].Name != "cache-stack" || groups[1].Action != provider.ActionUpdate {
		t.Errorf("the cache group = %+v, want the stale stack updated", groups[1])
	}
	if groups[2].Action != provider.ActionDelete || groups[2].Part != fake.PartImages {
		t.Errorf("the images group = %+v, want the dropped part deleted", groups[2])
	}
}
