package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/transform"
)

type publishedReader struct {
	links []providerkit.Link
	asked []string
}

func (r *publishedReader) Published(context.Context) ([]providerkit.Link, error) {
	return r.links, nil
}

func (r *publishedReader) Resolve(_ context.Context, link, property string) (string, error) {
	r.asked = append(r.asked, link+"."+property)
	for _, held := range r.links {
		if held.Name != link {
			continue
		}
		value, carries := held.Properties[property]
		if !carries {
			return "", providerkit.Refuse(providerkit.CodeInvalid, "link %s carries no property %q", link, property)
		}
		return value, nil
	}
	return "", providerkit.Refuse(providerkit.CodeInvalid, "nothing published %s", link)
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
	if len(links.asked) != 1 || links.asked[0] != "legacy.runtime" {
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
