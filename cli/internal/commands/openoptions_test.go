package commands

import (
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

func TestADeployChecksWhatItsTierNeeds(t *testing.T) {
	cfg := &project.Project{Slug: "shop"}

	production := OpenOptions{Intent: Deploy, Tier: environmentv1.Tier_TIER_PRODUCTION}.request(cfg)
	if !production.RequireHostname || production.CheckPreviewRoute {
		t.Errorf("production deploy request = %+v, want a hostname required and no preview route checked", production)
	}
	preview := OpenOptions{Intent: Deploy, Tier: environmentv1.Tier_TIER_PREVIEW}.request(cfg)
	if preview.RequireHostname || !preview.CheckPreviewRoute {
		t.Errorf("preview deploy request = %+v, want the preview route checked and no hostname required", preview)
	}
	if preview.Tier != environmentv1.Tier_TIER_PREVIEW {
		t.Errorf("preview deploy request tier = %v, want the tier the options name", preview.Tier)
	}
}

func TestOnlyADeployAsksTheProviderToCheckItsEdgeCanForwardItsHostnames(t *testing.T) {
	cfg := &project.Project{Slug: "shop"}

	for _, tier := range []environmentv1.Tier{environmentv1.Tier_TIER_PRODUCTION, environmentv1.Tier_TIER_PREVIEW} {
		if req := (OpenOptions{Intent: Deploy, Tier: tier}).request(cfg); !req.CheckForwarding {
			t.Errorf("%v deploy request = %+v, want the forwarding checked: a zone that cannot forward a hostname otherwise fails the deploy after it provisioned every stack", tier, req)
		}
	}
	if req := (OpenOptions{Intent: UseParts, Tier: environmentv1.Tier_TIER_PRODUCTION}).request(cfg); req.CheckForwarding {
		t.Errorf("request = %+v, want no forwarding checked by a command that forwards nothing", req)
	}
}

func TestOnlyAnExplicitDryRunPinsTheProviderAsADryRun(t *testing.T) {
	cfg := &project.Project{Slug: "shop"}

	if req := (OpenOptions{Intent: UseParts, DryRun: true}).request(cfg); !req.DryRun {
		t.Errorf("request = %+v, want the dry run the options ask for", req)
	}
	if req := (OpenOptions{Intent: UseParts}).request(cfg); req.DryRun {
		t.Errorf("request = %+v, want no dry run when the options ask for none", req)
	}
}
