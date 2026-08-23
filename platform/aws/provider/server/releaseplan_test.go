package server

import (
	"context"
	"strings"
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
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
	state := newRecordState()
	for slug, held := range map[string]edge.StackState{
		"shop": {Slug: "shop", GlobalPreview: "preview.acme.com"},
		"blog": {Slug: "blog", GlobalPreview: "preview.acme.com"},
	} {
		if err := state.WriteStack(ctx, edge.ClassPreview, slug, stackRecordOn(held)); err != nil {
			t.Fatalf("WriteStack(%s): %v", slug, err)
		}
	}

	live := map[string]int{"shop": 2, "blog": 1}
	previews := func(_ context.Context, slug string) (int, error) { return live[slug], nil }

	err := refusePreviewReleaseWhileServed(ctx, state, "preview.acme.com", previews)
	if err == nil {
		t.Fatal("refusePreviewReleaseWhileServed err = nil, want the release refused while previews are live on it")
	}
	for _, want := range []string{"*.preview.acme.com", "blog", "shop", "ocel preview rm", "ocel destroy --preview"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %q", err, want)
		}
	}

	live["blog"] = 0
	err = refusePreviewReleaseWhileServed(ctx, state, "preview.acme.com", previews)
	if err == nil || strings.Contains(err.Error(), "blog") {
		t.Fatalf("err = %v, want only shop still named once blog's last preview went", err)
	}

	live["shop"] = 0
	if err := refusePreviewReleaseWhileServed(ctx, state, "preview.acme.com", previews); err != nil {
		t.Fatalf("refusePreviewReleaseWhileServed after `ocel preview rm` took the last preview = %v, want it allowed", err)
	}

	for _, slug := range []string{"shop", "blog"} {
		if err := state.ForgetStack(ctx, edge.ClassPreview, slug); err != nil {
			t.Fatalf("ForgetStack(%s): %v", slug, err)
		}
	}
	if err := refusePreviewReleaseWhileServed(ctx, state, "preview.acme.com", previews); err != nil {
		t.Fatalf("refusePreviewReleaseWhileServed after `ocel destroy --preview` = %v, want it allowed", err)
	}
}

func TestReleasePlan(t *testing.T) {
	t.Parallel()

	recorded := previewWildcardOn("preview.acme.com", cloudflare.Kind)

	t.Run("describes the edge that holds the wildcard, not the project asking", func(t *testing.T) {
		t.Parallel()

		plan := releasePlan(planEdge(t, cloudflare.Kind), recorded)
		if plan.GetEdgeKind() != string(cloudflare.Kind) {
			t.Errorf("edge kind = %q, want %q", plan.GetEdgeKind(), cloudflare.Kind)
		}
		if plan.GetSubject() != recorded.BaseDomain {
			t.Errorf("subject = %q, want the base domain the operator types to confirm", plan.GetSubject())
		}
		itemFor(t, plan.GetItems(), "preview entry worker", "*.preview.acme.com")
	})

	t.Run("a wildcard no edge is known to hold is not planned for", func(t *testing.T) {
		t.Parallel()

		_, err := previewWildcardEdge(previewWildcardOn("preview.acme.com", ""), func(kind edge.Kind) (edge.Edge, error) {
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
	recorded := previewWildcardOn("preview.acme.com", "")
	recorded.Certificate = certs.Certificate{ARN: "arn:ocel"}
	recorded.Validation = domains.Records{Written: []edge.Record{written}, Owed: []edge.Record{owed}}

	t.Run("the CloudFront edge disables the wildcard distribution before deleting it", func(t *testing.T) {
		t.Parallel()

		items := releasePlanItems(planEdge(t, cloudfront.Kind), recorded)
		dist := itemFor(t, items, "wildcard distribution", "*.preview.acme.com")
		if dist.GetAction() != contractv1.RemovalItem_ACTION_DISABLE_THEN_DELETE || !dist.GetSlow() {
			t.Errorf("wildcard distribution = %v (slow=%v), want DISABLE_THEN_DELETE and slow", dist.GetAction(), dist.GetSlow())
		}
		if got := itemFor(t, items, "certificate", "arn:ocel").GetAction(); got != contractv1.RemovalItem_ACTION_DELETE {
			t.Errorf("certificate action = %v, want DELETE", got)
		}
		if got := itemFor(t, items, "DNS record", written.String()).GetAction(); got != contractv1.RemovalItem_ACTION_DELETE {
			t.Errorf("written record action = %v, want DELETE", got)
		}
		kept := itemFor(t, items, "DNS record", owed.String())
		if kept.GetAction() != contractv1.RemovalItem_ACTION_KEEP {
			t.Errorf("owed record action = %v, want KEEP", kept.GetAction())
		}
		if !strings.Contains(kept.GetReason(), "ocel never wrote it") {
			t.Errorf("owed record reason = %q, want it to say why it stays", kept.GetReason())
		}
	})

	t.Run("with no edge bought the wildcard domain name goes and the fallback stays", func(t *testing.T) {
		t.Parallel()

		items := releasePlanItems(planEdge(t, apigateway.Kind), recorded)
		if got := itemFor(t, items, "wildcard domain name", "*.preview.acme.com").GetAction(); got != contractv1.RemovalItem_ACTION_DELETE {
			t.Errorf("wildcard domain name action = %v, want DELETE", got)
		}
		if got := itemFor(t, items, "preview fallback API", "").GetAction(); got != contractv1.RemovalItem_ACTION_KEEP {
			t.Errorf("preview fallback API action = %v, want KEEP", got)
		}
	})

	t.Run("a certificate ocel adopted is left standing", func(t *testing.T) {
		t.Parallel()

		adopted := recorded
		adopted.Certificate = certs.Certificate{ARN: "arn:yours", Adopted: true}
		if got := itemFor(t, releasePlanItems(planEdge(t, cloudflare.Kind), adopted), "certificate", "arn:yours").GetAction(); got != contractv1.RemovalItem_ACTION_KEEP {
			t.Errorf("adopted certificate action = %v, want KEEP", got)
		}
	})
}
