package deploy

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/transform"
)

type publishedReader struct {
	links   []providerkit.Link
	failure error

	mu    sync.Mutex
	asked []string
}

func (r *publishedReader) Names(context.Context) ([]string, error) {
	names := make([]string, 0, len(r.links))
	for _, held := range r.links {
		names = append(names, held.Name)
	}
	return names, nil
}

func (r *publishedReader) Published(context.Context) ([]providerkit.Link, error) {
	return r.links, nil
}

func (r *publishedReader) Resolve(_ context.Context, link string) (providerkit.Link, error) {
	r.mu.Lock()
	r.asked = append(r.asked, link)
	r.mu.Unlock()
	if r.failure != nil {
		return providerkit.Link{}, r.failure
	}
	for _, held := range r.links {
		if held.Name == link {
			return held, nil
		}
	}
	return providerkit.Link{}, providerkit.Refuse(providerkit.CodeInvalid, "nothing published %s", link)
}

func planUnderTransform() providerkit.StackPlan {
	return providerkit.StackPlan{
		Ref: providerkit.StackRef{
			Project: "shop",
			Class:   providerkit.ClassProduction,
			Name:    naming.AppStack("production", "api", naming.NewRelease("dep1", "fp1")),
		},
		Kind: providerkit.StackApp,
		Resources: []providerkit.Resource{
			{Name: "db", Type: providerkit.LinkPostgres, Postgres: &providerkit.PostgresSpec{}},
			{Name: "uploads", Type: providerkit.LinkBucket, Bucket: &providerkit.BucketSpec{}},
		},
		App: &providerkit.AppPlan{
			App:       "api",
			Framework: "next",
			Functions: []providerkit.FunctionSpec{{Name: "fn--api--users"}},
		},
	}
}

type echoingEvaluator struct {
	patch func([]transform.Surfaces)
}

func (e echoingEvaluator) Evaluate(_ context.Context, req transform.Request) ([]transform.Result, error) {
	surfaces := make([]transform.Surfaces, len(req.Resources))
	for i, resource := range req.Resources {
		surfaces[i] = resource.Surfaces
	}
	if e.patch != nil {
		e.patch(surfaces)
	}
	results := make([]transform.Result, len(surfaces))
	for i, held := range overTheWire(surfaces) {
		results[i] = transform.Result{Surfaces: held}
	}
	return results, nil
}

func filledFromLink(link, property string) echoingEvaluator {
	return echoingEvaluator{patch: func(surfaces []transform.Surfaces) {
		surfaces[len(surfaces)-1]["lambda"]["runtime"] = map[string]any{
			outputPlaceholderKey: map[string]any{"link": link, "property": property},
		}
	}}
}

func TestAPassOverTheWholePlanOffersEveryResourceAndFunctionToTheTransform(t *testing.T) {
	evaluator := &fakeEvaluator{}

	if _, err := transformStackPlan(context.Background(), evaluator, planUnderTransform()); err != nil {
		t.Fatalf("transformStackPlan() = %v", err)
	}
	var seen []string
	for _, resource := range evaluator.seen.Resources {
		seen = append(seen, resource.Type+":"+resource.Name)
	}
	want := []string{"postgres:db", "bucket:uploads", "function:fn--api--users"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("the transform was offered %v, want %v — one pass over the whole plan", seen, want)
	}
	if evaluator.seen.Env != "production" || evaluator.seen.EnvClass != string(providerkit.ClassProduction) {
		t.Errorf("the transform was told env %q class %q, want the plan's own coordinate", evaluator.seen.Env, evaluator.seen.EnvClass)
	}
}

func TestAPlanWithNoTransformIsLeftExactlyAsItWasPlanned(t *testing.T) {
	transformed, err := transformStackPlan(context.Background(), nil, planUnderTransform())
	if err != nil {
		t.Fatalf("transformStackPlan() with no evaluator = %v", err)
	}
	if transformed != nil {
		t.Errorf("transformStackPlan() = %+v with no evaluator, want the planned arguments left alone", transformed)
	}
}

func TestATransformReadsALinkOutputThroughThePlansOwnLinks(t *testing.T) {
	links := &publishedReader{links: []providerkit.Link{
		{Type: providerkit.LinkPostgres, Name: "legacy", Properties: map[string]string{"runtime": "nodejs22.x"}},
	}}
	plan := planUnderTransform()
	plan.Links = links

	evaluator := filledFromLink("legacy", "runtime")

	transformed, err := transformStackPlan(context.Background(), evaluator, plan)
	if err != nil {
		t.Fatalf("transformStackPlan() = %v", err)
	}
	if transformed == nil {
		t.Fatal("transformStackPlan() applied nothing")
	}
	if got := transformed.functions["fn--api--users"].Runtime; got != "nodejs22.x" {
		t.Errorf("the function runs %q, want the value the published link carries", got)
	}
	if len(links.asked) != 1 || links.asked[0] != "legacy" {
		t.Fatalf("the pass asked the plan's links for %v, want the one output the transform named", links.asked)
	}
}

func TestATransformReadingALinkThisPlanProvisionsIsRefused(t *testing.T) {
	plan := planUnderTransform()
	plan.Links = &publishedReader{}

	evaluator := filledFromLink("db", "runtime")

	_, err := transformStackPlan(context.Background(), evaluator, plan)
	var provisioned *ProvisionedOutputError
	if !errors.As(err, &provisioned) {
		t.Fatalf("transformStackPlan() = %v, want it refused: this plan stands \"db\" up itself, so its outputs are not there to read", err)
	}
}

func TestATransformReadingAnUnpublishedLinkNamesWhatIsPublished(t *testing.T) {
	plan := planUnderTransform()
	plan.Links = &publishedReader{links: []providerkit.Link{
		{Type: providerkit.LinkBucket, Name: "archive", Properties: map[string]string{"bucket": "held"}},
	}}

	evaluator := filledFromLink("absent", "runtime")

	_, err := transformStackPlan(context.Background(), evaluator, plan)
	var unpublished *UnpublishedOutputError
	if !errors.As(err, &unpublished) {
		t.Fatalf("transformStackPlan() = %v, want an unpublished-output refusal", err)
	}
	if len(unpublished.Published) != 1 || unpublished.Published[0] != "archive" {
		t.Errorf("the refusal lists %v as published, want what the plan's links actually carry", unpublished.Published)
	}
}

func TestATransformReadingALinkThisPlanOnlyBindsIsRead(t *testing.T) {
	plan := planUnderTransform()
	plan.Resources[0].Linked = true
	plan.Links = &publishedReader{links: []providerkit.Link{
		{Type: providerkit.LinkPostgres, Name: "db", Properties: map[string]string{"runtime": "nodejs22.x"}},
	}}

	evaluator := filledFromLink("db", "runtime")

	transformed, err := transformStackPlan(context.Background(), evaluator, plan)
	if err != nil {
		t.Fatalf("transformStackPlan() = %v, want the bound link's own published record read", err)
	}
	if got := transformed.functions["fn--api--users"].Runtime; got != "nodejs22.x" {
		t.Errorf("the function runs %q, want the value the bound link's record carries", got)
	}
}

func TestAStoreThatFailsToResolveALinkIsNotReportedAsABadProperty(t *testing.T) {
	torn := errors.New("the record's pair is torn")
	plan := planUnderTransform()
	plan.Links = &publishedReader{
		links:   []providerkit.Link{{Type: providerkit.LinkPostgres, Name: "legacy"}},
		failure: torn,
	}

	_, err := transformStackPlan(context.Background(), filledFromLink("legacy", "runtime"), plan)
	if !errors.Is(err, torn) {
		t.Fatalf("transformStackPlan() = %v, want the store's own failure carried out", err)
	}
	var property *OutputPropertyError
	if errors.As(err, &property) {
		t.Error("a store that could not be read was reported as a record missing a property")
	}
}

func TestALinkCarryingNoSuchPropertyNamesWhatItDoesCarry(t *testing.T) {
	plan := planUnderTransform()
	plan.Links = &publishedReader{links: []providerkit.Link{{
		Type:       providerkit.LinkPostgres,
		Name:       "legacy",
		Properties: map[string]string{"host": "db.internal", "port": "5432"},
	}}}

	_, err := transformStackPlan(context.Background(), filledFromLink("legacy", "runtime"), plan)
	var property *OutputPropertyError
	if !errors.As(err, &property) {
		t.Fatalf("transformStackPlan() = %v, want an OutputPropertyError", err)
	}
	if want := []string{"host", "port"}; !slices.Equal(property.Carries, want) {
		t.Errorf("carries = %v, want the published record's own keys %v", property.Carries, want)
	}
}

func TestEveryOutputOffTheSameLinkResolvesItOnce(t *testing.T) {
	links := &publishedReader{links: []providerkit.Link{
		{Type: providerkit.LinkPostgres, Name: "legacy", Properties: map[string]string{"runtime": "nodejs22.x"}},
	}}
	plan := planUnderTransform()
	plan.Links = links

	evaluator := echoingEvaluator{patch: func(surfaces []transform.Surfaces) {
		placeholder := map[string]any{
			outputPlaceholderKey: map[string]any{"link": "legacy", "property": "runtime"},
		}
		surfaces[len(surfaces)-1]["lambda"]["runtime"] = placeholder
		surfaces[len(surfaces)-1]["lambda"]["handler"] = placeholder
	}}

	if _, err := transformStackPlan(context.Background(), evaluator, plan); err != nil {
		t.Fatalf("transformStackPlan() = %v", err)
	}
	if len(links.asked) != 1 {
		t.Errorf("the pass resolved %v, want one read for the one link both outputs name", links.asked)
	}
}
