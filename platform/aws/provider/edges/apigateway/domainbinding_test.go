package apigateway

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/router"
)

func TestBindDomainPublishesAFrontPerHost(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	e := bootstrapped(t, w)
	stack, err := e.Reconcile(ctx, testSpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	hosts := []string{"shop.example.com", "www.example.com"}
	for _, host := range hosts {
		if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: host, Certificate: "arn:aws:acm:eu-west-1:123456789012:certificate/abc"}); err != nil {
			t.Fatalf("BindDomain(%s): %v", host, err)
		}
	}

	records, err := edge.RecordsFor(edge.TargetFor(e, stack.State()), hosts)
	if err != nil {
		t.Fatalf("RecordsFor(%v): %v", hosts, err)
	}
	want := []edge.Record{
		{Name: hosts[0], Type: edge.RecordTypeCNAME, Value: regionalFront(hosts[0])},
		{Name: hosts[1], Type: edge.RecordTypeCNAME, Value: regionalFront(hosts[1])},
	}
	if !slices.Equal(records, want) {
		t.Errorf("records = %v, want %v: each API Gateway domain name has its own regional domain name", records, want)
	}

	if err := stack.UnbindDomain(ctx, hosts[0]); err != nil {
		t.Fatalf("UnbindDomain(%s): %v", hosts[0], err)
	}
	if _, err := edge.RecordsFor(edge.TargetFor(e, stack.State()), hosts[:1]); err == nil {
		t.Errorf("RecordsFor(%v) err = nil, want a refusal: the host is unbound and its front is gone", hosts[:1])
	}
}

func TestReconcileRecoversTheFrontOfADomainBoundBeforeItWasRecorded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	e := bootstrapped(t, w)
	stack, err := e.Reconcile(ctx, testSpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	const host = "shop.example.com"
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: host, Certificate: "arn:aws:acm:eu-west-1:123456789012:certificate/abc"}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}
	forgotten := stack.State()
	forgotten.PublishAddress(host, "")

	rerun, err := e.Reconcile(ctx, testSpec(), forgotten)
	if err != nil {
		t.Fatalf("Reconcile again: %v", err)
	}

	records, err := edge.RecordsFor(edge.TargetFor(e, rerun.State()), []string{host})
	if err != nil {
		t.Fatalf("RecordsFor after a reconcile of state predating the front: %v", err)
	}
	if want := regionalFront(host); len(records) != 1 || records[0].Value != want {
		t.Errorf("records = %v, want a CNAME to %q read back from the domain name API Gateway already has", records, want)
	}
}

func TestReconcileForgetsABindingWhoseDomainNameIsGone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	e := bootstrapped(t, w)
	stack, err := e.Reconcile(ctx, testSpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	const host = "shop.example.com"
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: host, Certificate: "arn:aws:acm:eu-west-1:123456789012:certificate/abc"}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}
	bound := stack.State()
	bound.PublishAddress(host, "")
	delete(w.gateway.domains, host)

	spec := testSpec()
	var warned []string
	spec.Warn = func(m string) { warned = append(warned, m) }
	rerun, err := e.Reconcile(ctx, spec, bound)
	if err != nil {
		t.Fatalf("Reconcile again: %v", err)
	}

	if hosts := rerun.State().Bound; slices.Contains(hosts, host) {
		t.Errorf("bound domains = %v, want %s forgotten: API Gateway has no domain name for it any more", hosts, host)
	}
	if len(warned) != 1 || !strings.Contains(warned[0], host) {
		t.Errorf("warned = %v, want the vanished domain name named", warned)
	}
}

func TestBindDomainAfterAPromotionMapsTheHostOntoThePromotedStage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	stack := reconciled(t, w)
	staged(t, stack, entryFunction, "shop/prod/web/r1111aaaa")
	promotion := router.Promotion{PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": "d1.f1"}}
	if err := openRouter(stack).MovePointer(ctx, router.PointerMove{Promotion: promotion}, progress.Discard()); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	host := "shop.example.com"
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: host, Certificate: "arn:aws:acm:eu-west-1:123456789012:certificate/abc"}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}

	api := w.gateway.named(productionAPIName())
	mapping, mapped := w.gateway.domains[host].mappings["(none)"]
	if !mapped || aws.ToString(mapping.RestApiId) != api.id || aws.ToString(mapping.Stage) != stageName {
		t.Fatalf("%s maps onto %+v, want the %s stage of %s: a hostname bound after the promotion serves it without another deploy", host, mapping, stageName, api.id)
	}
	if api.variables[entryVariable] != entryFunction {
		t.Errorf("the stage %s is mapped onto serves %q, want the promoted %q", host, api.variables[entryVariable], entryFunction)
	}
}
