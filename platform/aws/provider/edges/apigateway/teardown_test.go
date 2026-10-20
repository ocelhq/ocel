package apigateway

import (
	"context"
	"errors"
	"maps"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestTeardownRefusesWhileAProjectStillHasARestAPI(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	e := bootstrapped(t, w)
	stack, err := e.Reconcile(ctx, testSpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	err = e.Teardown(ctx, environment.TierProduction)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("Teardown = %v, want a refusal while %s still has a REST API", err, conformanceSlug)
	}
	if !strings.Contains(err.Error(), conformanceSlug) || !strings.Contains(err.Error(), "ocel destroy production") {
		t.Errorf("refusal = %q, want it to name the project and the command that clears it", err)
	}
	if err := e.Teardown(ctx, environment.TierPreview); err != nil {
		t.Errorf("Teardown(preview) = %v, want nil: the project's API belongs to the production tier", err)
	}

	if err := stack.Destroy(ctx); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if err := e.Teardown(ctx, environment.TierProduction); err != nil {
		t.Errorf("Teardown after the project's destroy = %v, want nil", err)
	}
}

func TestTeardownRefusesWhileAProjectStillHasAnUntaggedRestAPIItsStackRecorded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	e := bootstrapped(t, w)
	stack, err := e.Reconcile(ctx, testSpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	api := w.gateway.named(productionAPIName())
	if api == nil {
		t.Fatalf("Reconcile created no API named %s", productionAPIName())
	}
	api.tags = nil

	if _, err := e.Reconcile(ctx, testSpec(), stack.State()); err != nil {
		t.Fatalf("Reconcile over the untagged API: %v", err)
	}

	want := map[string]string{
		"ocel:managed-by": "ocel",
		"ocel:namespace":  string(defaultNamespace),
		"ocel:project":    conformanceSlug,
		"ocel:env-tier":   string(environment.TierProduction),
	}
	if !maps.Equal(api.tags, want) {
		t.Errorf("the recorded API carries the tags %v after Reconcile, want %v", api.tags, want)
	}
	err = e.Teardown(ctx, environment.TierProduction)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Errorf("Teardown = %v, want a refusal while %s still has the REST API its stack recorded", err, conformanceSlug)
	}
}

func TestDestroyReclaimsTheRestAPIThisStackRecorded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	e := bootstrapped(t, w)
	stack, err := e.Reconcile(ctx, testSpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	api := w.gateway.named(productionAPIName())
	if api == nil {
		t.Fatalf("Reconcile created no API named %s", productionAPIName())
	}
	api.name = "legacy--" + conformanceSlug + "--prod"

	if err := stack.Destroy(ctx); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if w.gateway.apis[api.id] != nil {
		t.Errorf("Destroy left %s in place: a stack reaches the API it recorded by id, not only by the name this run mints", api.id)
	}
}

func TestTeardownLeavesTheRestAPIsAnotherNamespaceFronts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	e := bootstrapped(t, w)
	if _, err := w.gateway.CreateRestApi(ctx, &apigateway.CreateRestApiInput{
		Name: aws.String(must(apiName("other", "shop", environment.TierProduction, ""))),
		Tags: map[string]string{
			"ocel:managed-by": "ocel",
			"ocel:namespace":  "other",
			"ocel:project":    "shop",
			"ocel:env-tier":   string(environment.TierProduction),
		},
	}); err != nil {
		t.Fatalf("CreateRestApi: %v", err)
	}

	if err := e.Teardown(ctx, environment.TierProduction); err != nil {
		t.Errorf("Teardown = %v, want nil: shop is fronted by the other namespace's bootstrap, not this one", err)
	}
}

func seedOtherProjectsAPIs(t *testing.T, w *world, tier environment.Tier, count int) []string {
	t.Helper()
	var ids []string
	for i := range count {
		slug := "other" + strconv.Itoa(i)
		created, err := w.gateway.CreateRestApi(context.Background(), &apigateway.CreateRestApiInput{
			Name: aws.String(must(apiName(defaultNamespace, slug, tier, previewPoint))),
			Tags: map[string]string{
				"ocel:managed-by": "ocel",
				"ocel:namespace":  string(defaultNamespace),
				"ocel:project":    slug,
				"ocel:env-tier":   string(tier),
				"ocel:env":        previewPoint,
			},
		})
		if err != nil {
			t.Fatalf("CreateRestApi: %v", err)
		}
		ids = append(ids, aws.ToString(created.Id))
	}
	return ids
}

func seedCustomerAPIs(t *testing.T, w *world, count int) {
	t.Helper()
	for i := range count {
		if _, err := w.gateway.CreateRestApi(context.Background(), &apigateway.CreateRestApiInput{
			Name: aws.String("customer-api-" + strconv.Itoa(i)),
		}); err != nil {
			t.Fatalf("CreateRestApi: %v", err)
		}
	}
}

func TestTeardownListsAsMuchHoweverManyRestAPIsTheAccountHolds(t *testing.T) {
	t.Parallel()

	teardownAmong := func(others int) int {
		w := newWorld()
		e := bootstrapped(t, w)
		seedCustomerAPIs(t, w, others)
		w.gateway.calls = nil

		if err := e.Teardown(context.Background(), environment.TierProduction); err != nil {
			t.Fatalf("Teardown = %v, want nil: no project of this namespace holds a REST API", err)
		}
		return countListings(w)
	}

	alone, among := teardownAmong(0), teardownAmong(20)
	if alone != among {
		t.Errorf("Teardown lists %d times beside no other REST APIs and %d times beside 20 of them; its listing must be bounded by its own namespace", alone, among)
	}
}

func countListings(w *world) int {
	return w.gateway.count("GetTaggedResources")
}

func TestDestroyListsAsMuchHoweverManyRestAPIsOtherProjectsHold(t *testing.T) {
	t.Parallel()

	destroyAmong := func(others int) int {
		ctx := context.Background()
		w := newWorld()
		_, stack := previewing(t, w)
		promotePreview(t, stack, previewPoint)
		kept := seedOtherProjectsAPIs(t, w, environment.TierPreview, others)
		w.gateway.calls = nil

		if err := stack.Destroy(ctx); err != nil {
			t.Fatalf("Destroy: %v", err)
		}
		if w.gateway.named(previewAPIName) != nil {
			t.Errorf("the REST API for %s survived the destroy", previewPoint)
		}
		for _, id := range kept {
			if w.gateway.apis[id] == nil {
				t.Errorf("Destroy deleted %s, which another project holds", id)
			}
		}
		return countListings(w)
	}

	alone, among := destroyAmong(0), destroyAmong(20)
	if alone != among {
		t.Errorf("Destroy lists %d times beside no other project's REST APIs and %d times beside 20 of them; its listing must be bounded by its own project", alone, among)
	}
}

func TestTeardownLeavesTheCoreStacksResourcesToCloudFormation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		t.Run(string(tier), func(t *testing.T) {
			t.Parallel()

			w := newWorld()
			e := w.edge()
			if _, err := e.Bootstrap(ctx, tier); err != nil {
				t.Fatalf("Bootstrap: %v", err)
			}
			w.gateway.calls = nil

			if err := e.Teardown(ctx, tier); err != nil {
				t.Fatalf("Teardown: %v", err)
			}
			if got := w.gateway.mutations(); len(got) != 0 {
				t.Errorf("teardown called %v, want nothing; the role and the 404 responder go down with the core stack", got)
			}
		})
	}

	t.Run("an unknown tier is refused rather than reported done", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		if err := w.edge().Teardown(ctx, environment.Tier("staging")); err == nil {
			t.Fatal("Teardown(staging) reported success having removed nothing")
		}
		if len(w.gateway.calls) != 0 {
			t.Errorf("an unknown tier called %v, want nothing", w.gateway.calls)
		}
	})
}
