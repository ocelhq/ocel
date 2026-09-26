package cloudfront

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/surface"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestADistributionNameFitsTheCommentWithoutLosingTheFieldsTheGateReads(t *testing.T) {
	t.Parallel()

	ns := bootstrap.Namespace(strings.Repeat("a", provider.MaxNamespaceLength))
	slug := strings.Repeat("s", 120)

	name := distributionName(ns, slug, edge.ClassProduction)
	if len(name) > maxDistributionNameLen {
		t.Fatalf("distributionName is %d characters and CloudFront allows %d, so the comment is cut where it lands: %q", len(name), maxDistributionNameLen, name)
	}
	if projects := surface.ProjectsNamed(ns, []string{name}, edge.ClassProduction); len(projects) != 1 {
		t.Fatalf("the gate reads %v out of %q, so this namespace does not see its own project", projects, name)
	}
	if other := distributionName(ns, slug[:len(slug)-1]+"t", edge.ClassProduction); other == name {
		t.Errorf("two projects both mint %q, so each would adopt the other's distribution", name)
	}
}

func TestTheCommentOnADistributionIsTheOwnerAProjectClaims(t *testing.T) {
	t.Parallel()

	ns := bootstrap.Namespace(strings.Repeat("a", provider.MaxNamespaceLength))
	slug := strings.Repeat("s", 120)
	p := &cloudFront{ns: ns}

	spec := distributionSpec{name: distributionName(ns, slug, edge.ClassProduction)}
	comment := aws.ToString(spec.config(nil, "").Comment)
	if owner := p.ProjectOwner(slug, edge.ClassProduction); comment != owner {
		t.Errorf("a distribution reads as owned by %q while the project claims %q, so the project is refused its own domain", comment, owner)
	}
}

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
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("Teardown = %v, want a refusal while %s still has a distribution", err, conformanceSlug)
	}
	if !strings.Contains(err.Error(), conformanceSlug) || !strings.Contains(err.Error(), "ocel destroy production") {
		t.Errorf("refusal = %q, want it to name the project and the command that clears it", err)
	}
	if err := e.Teardown(ctx, edge.ClassPreview); err != nil {
		t.Errorf("Teardown(preview) = %v, want nil: the existing distribution belongs to the production class", err)
	}

	if err := stack.Destroy(ctx); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if err := e.Teardown(ctx, edge.ClassProduction); err != nil {
		t.Errorf("Teardown after the project's destroy = %v, want nil", err)
	}
}

func TestTeardownLeavesTheDistributionsAnotherNamespaceFronts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	e := bootstrapped(t, w)
	if _, err := w.front.CreateDistribution(ctx, &cloudfront.CreateDistributionInput{
		DistributionConfig: &cftypes.DistributionConfig{
			Comment: aws.String(distributionName("other", "shop", edge.ClassProduction)),
		},
	}); err != nil {
		t.Fatalf("CreateDistribution: %v", err)
	}

	if err := e.Teardown(ctx, edge.ClassProduction); err != nil {
		t.Errorf("Teardown = %v, want nil: shop is fronted by the other namespace's bootstrap, not this one", err)
	}
}
