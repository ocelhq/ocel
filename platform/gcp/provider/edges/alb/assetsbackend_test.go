package alb

import (
	"context"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
)

var errAgentUnseen = errors.New("updating urn:pulumi:production::ocel-alb::gcp:projects/iAMMember:IAMMember::ocel-alb-production-reader: " +
	"Error applying IAM policy for project \"acme-prod\": Error setting IAM policy for project \"acme-prod\": " +
	"googleapi: Error 400: Service account service-4242@https-lb.iam.gserviceaccount.com does not exist., badRequest")

func TestABootstrapRaisedBeforeGoogleHasMadeTheLoadBalancersAgentRaisesItAgainOnceItExists(t *testing.T) {
	t.Parallel()

	balancer, w := balancing(t)
	w.failUps(albresources.LoadBalancerStack(environment.TierProduction), errAgentUnseen, errAgentUnseen)

	if _, err := balancer.Bootstrap(context.Background(), environment.TierProduction); err != nil {
		t.Fatalf("Bootstrap = %v, want it to wait for the agent the first backend bucket brings into being", err)
	}
	if got := w.waited(); got != 2 {
		t.Errorf("the bootstrap waited %d times, want 2: once after each raise refused for the missing agent", got)
	}
}

func TestABootstrapRefusedForAnythingButTheMissingAgentIsNotRaisedAgain(t *testing.T) {
	t.Parallel()

	balancer, w := balancing(t)
	refused := errors.New("googleapi: Error 403: Required 'compute.backendBuckets.create' permission, forbidden")
	w.failUps(albresources.LoadBalancerStack(environment.TierProduction), refused)

	if _, err := balancer.Bootstrap(context.Background(), environment.TierProduction); !errors.Is(err, refused) {
		t.Fatalf("Bootstrap = %v, want %v", err, refused)
	}
	if got := w.waited(); got != 0 {
		t.Errorf("the bootstrap waited %d times, want none: only the agent's absence passes with time", got)
	}
}
