package apigateway

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	agv2types "github.com/aws/aws-sdk-go-v2/service/apigatewayv2/types"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestPutHostRuleRetargetsAHostWithoutEverDroppingItsRule(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	previewing(t, w)
	host := previewHostname()
	if err := putHostRule(ctx, w.clients(), previewWild, host, "api-one", 0); err != nil {
		t.Fatalf("putHostRule: %v", err)
	}
	first := rulesOn(t, w, previewWild)[host]
	w.gateway.calls = nil

	if err := putHostRule(ctx, w.clients(), previewWild, host, "api-two", 0); err != nil {
		t.Fatalf("putHostRule again: %v", err)
	}

	assertSet(t, "the calls retargeting a host makes", w.gateway.mutations(), []string{
		"PutRoutingRule " + previewWild + " " + host,
	})
	held := rulesOn(t, w, previewWild)[host]
	if held == nil {
		t.Fatalf("%s lost its rule while being retargeted; the wildcard holds %v", host, slices.Sorted(maps.Keys(rulesOn(t, w, previewWild))))
	}
	if held.id != first.id || held.priority != first.priority {
		t.Errorf("rule = %+v, want the one %s already had updated in place: a delete-then-create 404s the host in between", held, host)
	}
	if held.api != "api-two" {
		t.Errorf("rule serves %s, want api-two", held.api)
	}
}

func TestPutHostRuleReprobesWhenAnotherDeployTakesThePriority(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	previewing(t, w)
	host := previewHostname()
	var stolen int32
	w.gateway.beforeRule = func(g *fakeGateway, priority int32) {
		if stolen != 0 {
			return
		}
		stolen = priority
		g.next++
		id := "rival" + strconv.Itoa(g.next)
		g.domains[previewWild].rules[id] = &fakeRule{
			id:       id,
			priority: priority,
			header:   hostHeader,
			host:     "rival--pr9." + previewBase,
			api:      "rival-api",
			stage:    stageName,
		}
	}

	w.gateway.calls = nil

	if err := putHostRule(ctx, w.clients(), previewWild, host, "api-one", 0); err != nil {
		t.Fatalf("putHostRule: %v", err)
	}

	held := rulesOn(t, w, previewWild)[host]
	if held == nil {
		t.Fatal("the deploy that lost the priority race left the host unrouted")
	}
	if held.priority == stolen {
		t.Errorf("priority = %d, want another one: a rival deploy took %d between the list and the create", held.priority, stolen)
	}
	if got := w.gateway.count("CreateRoutingRule"); got != 2 {
		t.Errorf("CreateRoutingRule calls = %d, want the conflict re-probed once", got)
	}
}

func TestPutHostRuleRefusesAWildcardNobodyClaimed(t *testing.T) {
	t.Parallel()

	w := newWorld()
	err := putHostRule(context.Background(), w.clients(), previewWild, previewHostname(), "api-one", 0)
	if err == nil {
		t.Fatal("putHostRule succeeded against a domain name that does not exist")
	}
	if !strings.Contains(err.Error(), "ocel domain use --preview") {
		t.Errorf("error = %v, want it to name the command that claims the wildcard", err)
	}
}

func TestPutHostRuleReadsTheHostHeaderWhateverItsCase(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	previewing(t, w)
	host := previewHostname()
	if _, err := w.routing.CreateRoutingRule(ctx, &apigatewayv2.CreateRoutingRuleInput{
		DomainName: aws.String(previewWild),
		Priority:   aws.Int32(42),
		Conditions: []agv2types.RoutingRuleCondition{{
			MatchHeaders: &agv2types.RoutingRuleMatchHeaders{
				AnyOf: []agv2types.RoutingRuleMatchHeaderValue{{
					Header:    aws.String("Host"),
					ValueGlob: aws.String(host),
				}},
			},
		}},
		Actions: []agv2types.RoutingRuleAction{{
			InvokeApi: &agv2types.RoutingRuleActionInvokeApi{ApiId: aws.String("api-one"), Stage: aws.String(stageName)},
		}},
	}); err != nil {
		t.Fatalf("CreateRoutingRule: %v", err)
	}
	w.gateway.calls = nil

	if err := putHostRule(ctx, w.clients(), previewWild, host, "api-one", 0); err != nil {
		t.Fatalf("putHostRule: %v", err)
	}

	if got := w.gateway.mutations(); len(got) != 0 {
		t.Errorf("the second rule %v was written; a host header the service spells differently still names the same host, and the duplicate would outlive the preview it serves", got)
	}
}

func TestDestroyTakesARuleTheLedgerNeverRecorded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	_, stack := previewing(t, w)
	promotePreview(t, stack, previewPoint)
	orphan := edge.SharedPreview(conformanceSlug, previewBase).Host("pr-crashed", "")
	if err := putHostRule(ctx, w.clients(), previewWild, orphan, "api-crashed", 0); err != nil {
		t.Fatalf("putHostRule: %v", err)
	}
	neighbour := edge.SharedPreview("other", previewBase).Host("pr1", "")
	if err := putHostRule(ctx, w.clients(), previewWild, neighbour, "api-neighbour", 0); err != nil {
		t.Fatalf("putHostRule: %v", err)
	}
	w.gateway.calls = nil
	w.gateway.pageSize = len(w.gateway.domains[previewWild].rules)

	if err := stack.Destroy(ctx); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	left := rulesOn(t, w, previewWild)
	if left[orphan] != nil {
		t.Errorf("%s is still routed: the promote that wrote it died before the ledger recorded the pointer, so a destroy driven off the ledger leaves it answering for a deleted API", orphan)
	}
	if left[previewHostname()] != nil {
		t.Errorf("%s is still routed after the stack that served it was destroyed", previewHostname())
	}
	if left[neighbour] == nil {
		t.Errorf("destroying one project took %s with it; the wildcard is shared by every project on the bootstrap", neighbour)
	}
	if left[anyHost] == nil {
		t.Error("destroying one project's stack took the catch-all rule with it")
	}
	if got := w.gateway.count("ListRoutingRules"); got != 1 {
		t.Errorf("ListRoutingRules calls = %d, want the shared wildcard listed once: that listing grows with every preview in the account, not with this project's pointers", got)
	}
}

func TestDomainOwnerNamesWhoAnswersARoutingRuleDomain(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("the shared preview entry once the catch-all is installed", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		e, _ := previewing(t, w)
		owner, err := e.DomainOwner(ctx, previewWild)
		if err != nil {
			t.Fatalf("DomainOwner: %v", err)
		}
		if owner != edge.PreviewEntryOwner {
			t.Errorf("DomainOwner(%q) = %q, want %q: a routing-rule domain carries no base path mapping, and a deploy refuses to publish a preview onto a wildcard nothing owns", previewWild, owner, edge.PreviewEntryOwner)
		}
	})

	t.Run("nobody while the catch-all is missing", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		e, _ := previewing(t, w)
		if err := deleteHostRule(ctx, w.clients(), previewWild, anyHost); err != nil {
			t.Fatalf("deleteHostRule: %v", err)
		}
		owner, err := e.DomainOwner(ctx, previewWild)
		if err != nil {
			t.Fatalf("DomainOwner: %v", err)
		}
		if owner != "" {
			t.Errorf("DomainOwner(%q) = %q, want nothing: without the catch-all, a hostname no preview claims reaches no API at all", previewWild, owner)
		}
	})
}

func TestThrottledKnowsTheRoutingRuleThrottle(t *testing.T) {
	t.Parallel()

	if !throttled(&agv2types.TooManyRequestsException{Message: aws.String("slow down")}) {
		t.Error("a routing-rule throttle was not recognised, so it reaches the user as a bare AWS error instead of the wait-and-retry advice")
	}
}
