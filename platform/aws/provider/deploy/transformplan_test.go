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
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/transformkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type publishedReader struct {
	bindings []providerkit.Binding
	failure  error

	mu    sync.Mutex
	asked []string
}

func (r *publishedReader) Names(context.Context) ([]string, error) {
	names := make([]string, 0, len(r.bindings))
	for _, held := range r.bindings {
		names = append(names, held.Name)
	}
	return names, nil
}

func (r *publishedReader) Published(context.Context) ([]providerkit.Binding, error) {
	return r.bindings, nil
}

func (r *publishedReader) Named(_ context.Context, binding string) (providerkit.Binding, error) {
	r.mu.Lock()
	r.asked = append(r.asked, binding)
	r.mu.Unlock()
	if r.failure != nil {
		return providerkit.Binding{}, r.failure
	}
	for _, held := range r.bindings {
		if held.Name == binding {
			return held, nil
		}
	}
	return providerkit.Binding{}, refusal.Refuse(refusal.CodeInvalid, "nothing published %s", binding)
}

func planUnderTransform() providerkit.StackPlan {
	return providerkit.StackPlan{
		Ref: providerkit.StackRef{
			Project: "shop",
			Class:   edge.ClassProduction,
			Name:    naming.AppStack("production", "api", naming.NewRelease("dep1", "fp1")),
		},
		Kind: providerkit.StackApp,
		Resources: []providerkit.Resource{
			{Name: "db", Type: providerkit.BindingPostgres, Postgres: &providerkit.PostgresSpec{}},
			{Name: "uploads", Type: providerkit.BindingBucket, Bucket: &providerkit.BucketSpec{}},
		},
		App: &providerkit.AppPlan{
			App:       "api",
			Framework: "next",
			Functions: []providerkit.FunctionSpec{{Name: "fn--api--users"}},
		},
	}
}

type patchingPass struct {
	patch func([]transformkit.Patches)
}

func (e patchingPass) Evaluate(_ context.Context, req transformkit.Request) ([]transformkit.Result, error) {
	patches := make([]transformkit.Patches, len(req.Resources))
	for i := range req.Resources {
		patches[i] = transformkit.Patches{}
	}
	if e.patch != nil {
		e.patch(patches)
	}
	results := make([]transformkit.Result, len(patches))
	for i, held := range overTheWire(patches) {
		results[i] = transformkit.Result{Patches: held}
	}
	return results, nil
}

func placeholderFor(kind, name, property string) map[string]any {
	return map[string]any{
		outputPlaceholderKey: map[string]any{"type": kind, "name": name, "property": property},
	}
}

func filledFromBinding(kind, name, property string) patchingPass {
	return patchingPass{patch: func(patches []transformkit.Patches) {
		patches[len(patches)-1]["lambda"] = map[string]any{"runtime": placeholderFor(kind, name, property)}
	}}
}

func offered(t *testing.T, plan providerkit.StackPlan) (*fakePass, []string) {
	t.Helper()
	pass := &fakePass{}
	if _, err := transformStackPlan(context.Background(), pass, plan); err != nil {
		t.Fatalf("transformStackPlan() = %v", err)
	}
	var seen []string
	for _, resource := range pass.seen.Resources {
		seen = append(seen, resource.Type+":"+resource.Name)
	}
	return pass, seen
}

func TestAnAppStackOffersOnlyTheFunctionsItStandsUp(t *testing.T) {
	pass, seen := offered(t, planUnderTransform())

	want := []string{"function:fn--api--users"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("the app stack offered %v, want %v — a patch on a resource this stack never constructs reaches nothing", seen, want)
	}
	if pass.seen.Provider != transformProvider {
		t.Errorf("the transform was told provider %q, want %q", pass.seen.Provider, transformProvider)
	}
	if pass.seen.Env != "production" || pass.seen.EnvClass != string(edge.ClassProduction) {
		t.Errorf("the transform was told env %q class %q, want the plan's own coordinate", pass.seen.Env, pass.seen.EnvClass)
	}
}

func TestAPlanWithNoTransformIsLeftExactlyAsItWasPlanned(t *testing.T) {
	transformed, err := transformStackPlan(context.Background(), nil, planUnderTransform())
	if err != nil {
		t.Fatalf("transformStackPlan() with no pass = %v", err)
	}
	if transformed != nil {
		t.Errorf("transformStackPlan() = %+v with no pass, want the planned arguments left alone", transformed)
	}
}

func TestAPatchLandsOnThePulumiResourceThatOcelConstructsForIt(t *testing.T) {
	pass := patchingPass{patch: func(patches []transformkit.Patches) {
		patches[len(patches)-1]["lambda"] = map[string]any{"memorySize": 2048}
	}}

	transformed, err := transformStackPlan(context.Background(), pass, planUnderTransform())
	if err != nil {
		t.Fatalf("transformStackPlan() = %v", err)
	}
	names := functionResourceNames("shop", planUnderTransform().Ref.Name, "fn--api--users")
	patch, claimed := transformed.patches[names["lambda"]]
	if !claimed {
		t.Fatalf("nothing was claimed for %q, want the lambda patch", names["lambda"])
	}
	if patch["memorySize"] != float64(2048) {
		t.Errorf("memorySize = %v, want 2048", patch["memorySize"])
	}
}

func TestAnInfraStackOffersTheResourcesItStandsUpAndNotTheOnesItIsHandled(t *testing.T) {
	plan := planUnderTransform()
	plan.App = nil
	plan.Kind = providerkit.StackInfra
	plan.Resources[0].Binding = "legacy-orders"

	_, seen := offered(t, plan)

	want := []string{"bucket:uploads"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("the infra stack offered %v, want %v — a bound resource is somebody else's to patch", seen, want)
	}
}

func TestAPatchOnAResourceThisProviderNeverConstructsIsRefused(t *testing.T) {
	plan := planUnderTransform()
	plan.App = nil
	plan.Kind = providerkit.StackInfra
	pass := patchingPass{patch: func(patches []transformkit.Patches) {
		patches[1]["queue"] = map[string]any{"fifo": true}
	}}

	_, err := transformStackPlan(context.Background(), pass, plan)
	if err == nil || !strings.Contains(err.Error(), "queue") {
		t.Fatalf("transformStackPlan() = %v, want the resource this provider never constructs refused by name", err)
	}
}

func TestABucketWithNoDeclaredOriginsCanStillBeGivenCORSByATransform(t *testing.T) {
	plan := planUnderTransform()
	plan.App = nil
	plan.Kind = providerkit.StackInfra
	pass := patchingPass{patch: func(patches []transformkit.Patches) {
		patches[1]["cors"] = map[string]any{"corsRules": []any{map[string]any{"allowedMethods": []any{"GET"}}}}
	}}

	transformed, err := transformStackPlan(context.Background(), pass, plan)
	if err != nil {
		t.Fatalf("transformStackPlan() = %v, want a transform allowed to add CORS to a bucket that declares no origins", err)
	}
	if !transformed.opensCORS("uploads") {
		t.Fatal("the bucket was not marked as opened by the patch, so the deploy would construct nothing for it")
	}
	names := bucketResourceNames("shop", "production", "uploads")
	if _, registered := transformed.patches[names["cors"]]; !registered {
		t.Error("nothing was registered for the bucket's cors resource")
	}
}

func TestATransformReadsABindingOutputThroughThePlansOwnBindings(t *testing.T) {
	bindings := &publishedReader{bindings: []providerkit.Binding{
		{Type: providerkit.BindingPostgres, Name: "legacy", Properties: map[string]string{"runtime": "nodejs22.x"}},
	}}
	plan := planUnderTransform()
	plan.Bindings = bindings

	transformed, err := transformStackPlan(context.Background(), filledFromBinding(customBindingType, "legacy", "runtime"), plan)
	if err != nil {
		t.Fatalf("transformStackPlan() = %v", err)
	}
	names := functionResourceNames("shop", plan.Ref.Name, "fn--api--users")
	if got := transformed.patches[names["lambda"]]["runtime"]; got != "nodejs22.x" {
		t.Errorf("the function runs %q, want the value the published binding carries", got)
	}
	if len(bindings.asked) != 1 || bindings.asked[0] != "legacy" {
		t.Fatalf("the pass asked the plan's bindings for %v, want the one output the transform named", bindings.asked)
	}
}

func TestATransformReadingABindingThisPlanProvisionsIsRefused(t *testing.T) {
	plan := planUnderTransform()
	plan.Bindings = &publishedReader{}

	_, err := transformStackPlan(context.Background(), filledFromBinding("postgres", "db", "runtime"), plan)
	var provisioned *ProvisionedOutputError
	if !errors.As(err, &provisioned) {
		t.Fatalf("transformStackPlan() = %v, want it refused: this plan stands \"db\" up itself, so its outputs are not there to read", err)
	}
}

func TestATransformReadingABindingThisProjectNeverBoundIsRefused(t *testing.T) {
	plan := planUnderTransform()
	plan.Resources[1].Binding = "archive"
	plan.Bindings = &publishedReader{}

	_, err := transformStackPlan(context.Background(), filledFromBinding("postgres", "orders", "runtime"), plan)
	var unbound *UnboundOutputError
	if !errors.As(err, &unbound) {
		t.Fatalf("transformStackPlan() = %v, want an unbound-output refusal", err)
	}
	if !slices.Equal(unbound.Declared, []string{"bucket.uploads"}) {
		t.Errorf("the refusal lists %v as bound, want what the config actually binds", unbound.Declared)
	}
}

func TestATransformReadingAnUnpublishedRecordNamesWhatIsPublished(t *testing.T) {
	plan := planUnderTransform()
	plan.Bindings = &publishedReader{bindings: []providerkit.Binding{
		{Type: providerkit.BindingBucket, Name: "archive", Properties: map[string]string{"bucket": "held"}},
	}}

	_, err := transformStackPlan(context.Background(), filledFromBinding(customBindingType, "absent", "runtime"), plan)
	var unpublished *UnpublishedOutputError
	if !errors.As(err, &unpublished) {
		t.Fatalf("transformStackPlan() = %v, want an unpublished-output refusal", err)
	}
	if !slices.Equal(unpublished.Carries, []string{"archive"}) {
		t.Errorf("the refusal lists %v as published, want what the plan's bindings actually carry", unpublished.Carries)
	}
}

func TestATransformReadsABoundResourceUnderTheNameItIsPublishedAs(t *testing.T) {
	plan := planUnderTransform()
	plan.Resources[0].Binding = "legacy-orders"
	plan.Bindings = &publishedReader{bindings: []providerkit.Binding{
		{Type: providerkit.BindingPostgres, Name: "legacy-orders", Properties: map[string]string{"runtime": "nodejs22.x"}},
	}}

	transformed, err := transformStackPlan(context.Background(), filledFromBinding("postgres", "db", "runtime"), plan)
	if err != nil {
		t.Fatalf("transformStackPlan() = %v, want the bound resource's own published record read", err)
	}
	names := functionResourceNames("shop", plan.Ref.Name, "fn--api--users")
	if got := transformed.patches[names["lambda"]]["runtime"]; got != "nodejs22.x" {
		t.Errorf("the function runs %q, want the value the bound record carries", got)
	}
}

func TestAStoreThatFailsToResolveABindingIsNotReportedAsABadProperty(t *testing.T) {
	torn := errors.New("the record's pair is torn")
	plan := planUnderTransform()
	plan.Bindings = &publishedReader{
		bindings: []providerkit.Binding{{Type: providerkit.BindingPostgres, Name: "legacy"}},
		failure:  torn,
	}

	_, err := transformStackPlan(context.Background(), filledFromBinding(customBindingType, "legacy", "runtime"), plan)
	if !errors.Is(err, torn) {
		t.Fatalf("transformStackPlan() = %v, want the store's own failure carried out", err)
	}
	var property *OutputPropertyError
	if errors.As(err, &property) {
		t.Error("a store that could not be read was reported as a record missing a property")
	}
}

func TestABindingCarryingNoSuchPropertyNamesWhatItDoesCarry(t *testing.T) {
	plan := planUnderTransform()
	plan.Bindings = &publishedReader{bindings: []providerkit.Binding{{
		Type:       providerkit.BindingPostgres,
		Name:       "legacy",
		Properties: map[string]string{"host": "db.internal", "port": "5432"},
	}}}

	_, err := transformStackPlan(context.Background(), filledFromBinding(customBindingType, "legacy", "runtime"), plan)
	var property *OutputPropertyError
	if !errors.As(err, &property) {
		t.Fatalf("transformStackPlan() = %v, want an OutputPropertyError", err)
	}
	if want := []string{"host", "port"}; !slices.Equal(property.Carries, want) {
		t.Errorf("carries = %v, want the published record's own keys %v", property.Carries, want)
	}
}

func TestEveryOutputOffTheSameBindingResolvesItOnce(t *testing.T) {
	bindings := &publishedReader{bindings: []providerkit.Binding{
		{Type: providerkit.BindingPostgres, Name: "legacy", Properties: map[string]string{"runtime": "nodejs22.x"}},
	}}
	plan := planUnderTransform()
	plan.Bindings = bindings

	pass := patchingPass{patch: func(patches []transformkit.Patches) {
		placeholder := placeholderFor(customBindingType, "legacy", "runtime")
		patches[len(patches)-1]["lambda"] = map[string]any{
			"runtime":     placeholder,
			"description": placeholder,
		}
	}}

	if _, err := transformStackPlan(context.Background(), pass, plan); err != nil {
		t.Fatalf("transformStackPlan() = %v", err)
	}
	if len(bindings.asked) != 1 {
		t.Errorf("the pass resolved %v, want one read for the one binding both outputs name", bindings.asked)
	}
}
