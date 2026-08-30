package box_test

import (
	"context"
	"strings"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const previewBase = "preview.example.com"

func previewSpec() edge.PreviewWildcardSpec {
	return edge.PreviewWildcardSpec{
		BaseDomain: previewBase,
		GrammarMin: edge.PreviewGrammarMin,
		GrammarMax: edge.PreviewGrammarMax,
	}
}

func TestThePreviewEntryBearsNoCertificateAndStillPublishesAFrontToPointAt(t *testing.T) {
	t.Parallel()

	_, front, _ := standing(t)
	ctx := context.Background()
	spec := previewSpec()
	if spec.Certificate != "" {
		t.Fatalf("this test is meant to reconcile a wildcard carrying no certificate and carries %q", spec.Certificate)
	}

	published, err := front.ReconcilePreviewWildcard(ctx, spec)
	if err != nil {
		t.Fatalf("ReconcilePreviewWildcard with no certificate = %v: a box terminates each preview hostname on its own http-01 certificate, so there is no wildcard certificate for this spec to carry", err)
	}
	if published != address {
		t.Fatalf("the wildcard published %q, want the box's address %q: %s resolves to one A record and it is the box", published, address, edge.PreviewWildcard(previewBase))
	}
	records, err := edge.RecordsFor(edge.DNSTarget{Kind: front.Kind(), Front: published}, []string{edge.PreviewWildcard(previewBase)})
	if err != nil {
		t.Fatalf("RecordsFor: %v", err)
	}
	want := edge.Record{Name: edge.PreviewWildcard(previewBase), Type: edge.RecordTypeA, Value: address}
	if len(records) != 1 || records[0] != want {
		t.Errorf("records = %v, want %v: a box's floor is one manual A record for the whole preview base", records, want)
	}
}

func TestTheWildcardIsOwnedByThePreviewEntryWhileItsRouteStandsAndByNobodyAfter(t *testing.T) {
	t.Parallel()

	_, front, _ := standing(t)
	ctx := context.Background()
	wildcard := edge.PreviewWildcard(previewBase)

	if owner, err := front.DomainOwner(ctx, wildcard); err != nil || owner != "" {
		t.Fatalf("DomainOwner(%s) = %q, %v before anything installed it, want nobody", wildcard, owner, err)
	}
	if _, err := front.ReconcilePreviewWildcard(ctx, previewSpec()); err != nil {
		t.Fatalf("ReconcilePreviewWildcard: %v", err)
	}
	owner, err := front.DomainOwner(ctx, wildcard)
	if err != nil {
		t.Fatalf("DomainOwner: %v", err)
	}
	if owner != edge.PreviewEntryOwner {
		t.Errorf("DomainOwner(%s) = %q, want %q: `ocel domain status` reads this to decide whether the wildcard route is installed, and a false answer prints MISSING on a box that is serving",
			wildcard, owner, edge.PreviewEntryOwner)
	}
	if err := front.DestroyPreviewWildcard(ctx, previewBase); err != nil {
		t.Fatalf("DestroyPreviewWildcard: %v", err)
	}
	if owner, err := front.DomainOwner(ctx, wildcard); err != nil || owner != "" {
		t.Errorf("DomainOwner(%s) = %q, %v once the route is gone, want nobody", wildcard, owner, err)
	}
}

func TestAPreviewWildcardWithNoBaseIsRefusedRatherThanInstalledAsADefaultRoute(t *testing.T) {
	t.Parallel()

	_, front, _ := standing(t)
	spec := previewSpec()
	spec.BaseDomain = ""

	_, err := front.ReconcilePreviewWildcard(context.Background(), spec)
	if err == nil {
		t.Fatal("a preview wildcard naming no base domain was installed, and the route it renders carries no host matcher: it receives every hostname pointed at this machine, a mistyped production hostname included")
	}
	if !strings.Contains(err.Error(), "host matcher") {
		t.Errorf("the refusal reads %q, and it is the empty-host default route it must name", err)
	}
}

func TestABoxAlreadyServingOnePreviewBaseRefusesASecondRatherThanSwappingIt(t *testing.T) {
	t.Parallel()

	_, front, _ := standing(t)
	ctx := context.Background()
	if _, err := front.ReconcilePreviewWildcard(ctx, previewSpec()); err != nil {
		t.Fatalf("ReconcilePreviewWildcard: %v", err)
	}
	other := previewSpec()
	other.BaseDomain = "previews.example.org"

	if _, err := front.ReconcilePreviewWildcard(ctx, other); err == nil {
		t.Fatal("a second preview base was installed over the first, and every preview hostname on this box is a name under the base it was raised on: swapping it silently takes every live preview off the air")
	}
}
