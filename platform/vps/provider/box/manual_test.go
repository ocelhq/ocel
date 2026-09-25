package box_test

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/box"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/manual"
)

func routedByHand() *machine {
	stood := aMachine()
	stood.byHand = manual.DefaultPort
	return stood
}

func TestABindOnABoxYourProxyFrontsSaysWhatToRouteToTheSwitchboard(t *testing.T) {
	t.Parallel()

	stood := routedByHand()
	stack := standingOn(t, stood, slug)
	var said []string
	if err := stack.BindDomain(context.Background(), edge.DomainBinding{
		Hostname: "shop.example.com",
		Say:      func(line string) { said = append(said, line) },
	}); err != nil {
		t.Fatalf("BindDomain() = %v", err)
	}
	if want := manual.Route("shop.example.com", manual.DefaultPort); !slices.Contains(said, want) {
		t.Errorf("the bind said %q, want %q: nothing reaches the hostname until your proxy routes it", said, want)
	}
}

func TestABindOnABoxOcelsOwnProxyFrontsAsksNothingOfYou(t *testing.T) {
	t.Parallel()

	stack := standingOn(t, aMachine(), slug)
	var said []string
	if err := stack.BindDomain(context.Background(), edge.DomainBinding{
		Hostname: "shop.example.com",
		Say:      func(line string) { said = append(said, line) },
	}); err != nil {
		t.Fatalf("BindDomain() = %v", err)
	}
	if len(said) != 0 {
		t.Errorf("the bind said %q, want nothing: ocel's own proxy takes the hostname up itself", said)
	}
}

func TestAPreviewWildcardOnABoxYourProxyFrontsSaysWhatToRoute(t *testing.T) {
	t.Parallel()

	front := edgeOver(routedByHand(), fake.NewRecords())
	var warned []string
	spec := previewSpec()
	spec.Warn = func(line string) { warned = append(warned, line) }
	if _, err := front.ReconcilePreviewWildcard(context.Background(), spec); err != nil {
		t.Fatalf("ReconcilePreviewWildcard() = %v", err)
	}
	if want := manual.Route(edge.PreviewWildcard(previewBase), manual.DefaultPort); !slices.Contains(warned, want) {
		t.Errorf("the wildcard said %q, want %q", warned, want)
	}
}

func TestACertificateYourProxyHoldsIsKeptAsYours(t *testing.T) {
	t.Parallel()

	front := edgeOver(routedByHand(), fake.NewRecords())
	for _, change := range front.ProjectRemovals(edge.ProjectScope{
		Slug: slug, Class: edge.ClassProduction, Hostnames: []string{"shop.example.com"}, Front: address,
	})[0].Changes {
		if change.Kind != box.CertificateKind {
			continue
		}
		if change.Name != certs.ProxyHandle("shop.example.com") || change.Action != edge.PlanKeep || change.Reason != "held by your proxy" {
			t.Errorf("the certificate row is %+v, want %s kept as held by your proxy", change, certs.ProxyHandle("shop.example.com"))
		}
	}
}
