package cloudfront

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestTeardownRefusesWhileAProjectStillHasADistribution(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	e := bootstrapped(t, w)
	stack, err := e.Reconcile(ctx, testSpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	err = e.Teardown(ctx, edge.ClassProduction)
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Fatalf("Teardown = %v, want a refusal while %s still has a distribution", err, conformanceSlug)
	}
	if !strings.Contains(err.Error(), conformanceSlug) || !strings.Contains(err.Error(), "ocel destroy production") {
		t.Errorf("refusal = %q, want it to name the project and the command that clears it", err)
	}
	if err := e.Teardown(ctx, edge.ClassPreview); err != nil {
		t.Errorf("Teardown(preview) = %v, want nil: the standing distribution belongs to the production class", err)
	}

	if err := stack.Destroy(ctx); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if err := e.Teardown(ctx, edge.ClassProduction); err != nil {
		t.Errorf("Teardown after the project's destroy = %v, want nil", err)
	}
}

func TestTeardownIgnoresADistributionAnotherBootstrapFronts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	e := bootstrapped(t, w)
	if _, err := w.front.CreateDistribution(ctx, &cloudfront.CreateDistributionInput{
		DistributionConfig: &cftypes.DistributionConfig{
			Comment: aws.String(distributionName("fronted-elsewhere", edge.ClassProduction)),
		},
	}); err != nil {
		t.Fatalf("CreateDistribution: %v", err)
	}

	if err := e.Teardown(ctx, edge.ClassProduction); err != nil {
		t.Errorf("Teardown = %v, want nil: this bootstrap's ledger names no project behind that distribution", err)
	}
}
