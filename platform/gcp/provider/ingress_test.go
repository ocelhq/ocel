package gcp_test

import (
	"testing"

	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
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

func TestAServiceBehindTheCloudflareWorkerAnswersItsRevisionTagURLToEveryCaller(t *testing.T) {
	t.Parallel()

	front := cloudflare.New("ocel", cloudflare.Options{})

	if got := gcp.IngressFor(front.Facts()); got != "INGRESS_TRAFFIC_ALL" {
		t.Errorf("a service behind the %s worker takes ingress %q, want every caller: the worker reaches the revision-tag URL itself, "+
			"and the runtime refuses any request without the tier's origin secret", cloudflare.Kind, got)
	}
}
