package gcp_test

import (
	"testing"

	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

func TestAServiceBehindTheLoadBalancerIsNotReachableOnItsRunAppURL(t *testing.T) {
	t.Parallel()

	facts := alb.New(alb.Deps{Project: "acme-prod"}).Facts()

	if got := gcp.IngressFor(facts); got != "INGRESS_TRAFFIC_INTERNAL_LOAD_BALANCER" {
		t.Errorf("a service behind the %s edge takes ingress %q, want internal and cloud load balancing: "+
			"the gate runs on the load balancer, and a service answering its run.app url would be a way round it", alb.Kind, got)
	}
}
