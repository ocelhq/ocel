package server

import (
	"context"
	"strings"
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/domains"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func planEdge(t *testing.T, kind edge.Kind) edge.Edge {
	t.Helper()
	edgeFront, err := edges.EdgeFor(kind, edges.Deps{})
	if err != nil {
		t.Fatalf("EdgeFor(%s): %v", kind, err)
	}
	return edgeFront
}

func TestRefusePreviewReleaseWhileProjectsAreServed(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ssmc := &stateSSM{params: map[string]string{}}
	for slug, state := range map[string]edge.StackState{
		"shop": {edge.StackKeySlug: "shop", edge.StackKeyGlobalPreview: "preview.acme.com"},
		"blog": {edge.StackKeySlug: "blog", edge.StackKeyGlobalPreview: "preview.acme.com"},
	} {
		if err := bootstrap.WriteStackStateFor(ctx, ssmc, bootstrap.ClassPreview, slug, state); err != nil {
			t.Fatalf("WriteStackStateFor(%s): %v", slug, err)
		}
	}

	live := map[string]int{"shop": 2, "blog": 1}
	previews := func(_ context.Context, slug string) (int, error) { return live[slug], nil }

	err := refusePreviewReleaseWhileServed(ctx, ssmc, "preview.acme.com", previews)
	if err == nil {
		t.Fatal("refusePreviewReleaseWhileServed err = nil, want the release refused while previews are live on it")
	}
	for _, want := range []string{"*.preview.acme.com", "blog", "shop", "ocel preview rm", "ocel destroy --preview"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %q", err, want)
		}
	}

	live["blog"] = 0
	err = refusePreviewReleaseWhileServed(ctx, ssmc, "preview.acme.com", previews)
	if err == nil || strings.Contains(err.Error(), "blog") {
		t.Fatalf("err = %v, want only shop still named once blog's last preview went", err)
	}

	live["shop"] = 0
	if err := refusePreviewReleaseWhileServed(ctx, ssmc, "preview.acme.com", previews); err != nil {
		t.Fatalf("refusePreviewReleaseWhileServed after `ocel preview rm` took the last preview = %v, want it allowed", err)
	}

	for _, slug := range []string{"shop", "blog"} {
		if err := bootstrap.DeleteStackStateFor(ctx, ssmc, bootstrap.ClassPreview, slug); err != nil {
			t.Fatalf("DeleteStackStateFor(%s): %v", slug, err)
		}
	}
	if err := refusePreviewReleaseWhileServed(ctx, ssmc, "preview.acme.com", previews); err != nil {
		t.Fatalf("refusePreviewReleaseWhileServed after `ocel destroy --preview` = %v, want it allowed", err)
	}
}

func TestReleaseEdgeStackPlan(t *testing.T) {
	t.Parallel()

	recorded := bootstrap.PreviewDomain{BaseDomain: "preview.acme.com", Edge: cloudflare.Kind}

	t.Run("describes the edge that holds the wildcard, not the project asking", func(t *testing.T) {
		t.Parallel()

		plan := releaseEdgeStackPlan(planEdge(t, cloudflare.Kind), recorded)
		if plan.GetEdgeKind() != string(cloudflare.Kind) {
			t.Errorf("edge kind = %q, want %q", plan.GetEdgeKind(), cloudflare.Kind)
		}
		itemFor(t, plan.GetItems(), "preview entry worker", "*.preview.acme.com")
	})

	t.Run("a wildcard no edge is known to hold is not planned for", func(t *testing.T) {
		t.Parallel()

		_, err := previewWildcardEdge(bootstrap.PreviewDomain{BaseDomain: "preview.acme.com"}, func(kind edge.Kind) (edge.Edge, error) {
			return planEdge(t, kind), nil
		})
		if err == nil {
			t.Fatal("previewWildcardEdge err = nil, want the plan refused rather than describing a guessed edge's teardown")
		}
	})
}

func TestReleasePlanItems(t *testing.T) {
	t.Parallel()

	owed := edge.Record{Name: "*.preview.acme.com", Type: edge.RecordTypeCNAME, Value: "d1.cloudfront.net"}
	written := edge.Record{Name: "_ocel.preview.acme.com", Type: edge.RecordTypeCNAME, Value: "_v.acm-validations.aws"}
	recorded := bootstrap.PreviewDomain{
		BaseDomain: "preview.acme.com",
		Settlement: domains.Settlement{
			Certificate: certs.Certificate{ARN: "arn:ocel"},
			Validation:  domains.Records{Written: []edge.Record{written}, Owed: []edge.Record{owed}},
		},
	}

	t.Run("the CloudFront edge disables the wildcard distribution before deleting it", func(t *testing.T) {
		t.Parallel()

		items := releasePlanItems(planEdge(t, cloudfront.Kind), recorded)
		dist := itemFor(t, items, "wildcard distribution", "*.preview.acme.com")
		if dist.GetAction() != deploymentsv1.TeardownItem_ACTION_DISABLE_THEN_DELETE || !dist.GetSlow() {
			t.Errorf("wildcard distribution = %v (slow=%v), want DISABLE_THEN_DELETE and slow", dist.GetAction(), dist.GetSlow())
		}
		if got := itemFor(t, items, "certificate", "arn:ocel").GetAction(); got != deploymentsv1.TeardownItem_ACTION_DELETE {
			t.Errorf("certificate action = %v, want DELETE", got)
		}
		if got := itemFor(t, items, "DNS record", written.String()).GetAction(); got != deploymentsv1.TeardownItem_ACTION_DELETE {
			t.Errorf("written record action = %v, want DELETE", got)
		}
		kept := itemFor(t, items, "DNS record", owed.String())
		if kept.GetAction() != deploymentsv1.TeardownItem_ACTION_KEEP {
			t.Errorf("owed record action = %v, want KEEP", kept.GetAction())
		}
		if !strings.Contains(kept.GetReason(), "ocel never wrote it") {
			t.Errorf("owed record reason = %q, want it to say why it stays", kept.GetReason())
		}
	})

	t.Run("with no edge bought the wildcard domain name goes and the fallback stays", func(t *testing.T) {
		t.Parallel()

		items := releasePlanItems(planEdge(t, apigateway.Kind), recorded)
		if got := itemFor(t, items, "wildcard domain name", "*.preview.acme.com").GetAction(); got != deploymentsv1.TeardownItem_ACTION_DELETE {
			t.Errorf("wildcard domain name action = %v, want DELETE", got)
		}
		if got := itemFor(t, items, "preview fallback API", "").GetAction(); got != deploymentsv1.TeardownItem_ACTION_KEEP {
			t.Errorf("preview fallback API action = %v, want KEEP", got)
		}
	})

	t.Run("a certificate ocel adopted is left standing", func(t *testing.T) {
		t.Parallel()

		adopted := recorded
		adopted.Settlement.Certificate = certs.Certificate{ARN: "arn:yours", Adopted: true}
		if got := itemFor(t, releasePlanItems(planEdge(t, cloudflare.Kind), adopted), "certificate", "arn:yours").GetAction(); got != deploymentsv1.TeardownItem_ACTION_KEEP {
			t.Errorf("adopted certificate action = %v, want KEEP", got)
		}
	})
}
