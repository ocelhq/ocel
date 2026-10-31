package albresources

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
)

func TestTwoNamespacesNameTheirLoadBalancersApart(t *testing.T) {
	t.Parallel()

	ours, theirs := LoadBalancerNames("ocel", environment.TierProduction), LoadBalancerNames("ocel-dev", environment.TierProduction)
	if ours.URLMap == theirs.URLMap || ours.Address == theirs.Address {
		t.Errorf("namespaces ocel and ocel-dev both name their load balancer %q, and the second bootstrap would take over the first's", ours.URLMap)
	}
}

func TestTheURLMapPathNamesTheTiersLoadBalancerURLMap(t *testing.T) {
	t.Parallel()

	for tier, want := range map[environment.Tier]string{
		environment.TierProduction: "projects/acme-prod/global/urlMaps/ocel-prod-alb-routes",
		environment.TierPreview:    "projects/acme-prod/global/urlMaps/ocel-preview-alb-routes",
	} {
		if got := URLMapPath("ocel", "acme-prod", tier); got != want {
			t.Errorf("URLMapPath(ocel, acme-prod, %s) = %q, want %q", tier, got, want)
		}
	}
}
