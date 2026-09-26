package deploy

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/transformkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type publishedReader struct {
	bindings []provider.Binding
	failure  error

	mu    sync.Mutex
	asked []string
}

func (r *publishedReader) Names(context.Context) ([]string, error) {
	names := make([]string, 0, len(r.bindings))
	for _, record := range r.bindings {
		names = append(names, record.Name)
	}
	return names, nil
}

func (r *publishedReader) Published(context.Context) ([]provider.Binding, error) {
	return r.bindings, nil
}

func (r *publishedReader) Named(_ context.Context, binding string) (provider.Binding, error) {
	r.mu.Lock()
	r.asked = append(r.asked, binding)
	r.mu.Unlock()
	if r.failure != nil {
		return provider.Binding{}, r.failure
	}
	for _, record := range r.bindings {
		if record.Name == binding {
			return record, nil
		}
	}
	return provider.Binding{}, refusal.Refuse(refusal.CodeInvalid, "nothing published %s", binding)
}

func specUnderTransform() provider.StackSpec {
	return provider.StackSpec{
		Ref: provider.StackRef{
			Project: "shop",
			Class:   edge.ClassProduction,
			Name:    naming.AppStack("production", "api", naming.NewRelease("dep1", "fp1")),
		},
		Kind: provider.StackApp,
		Resources: []provider.Resource{
			{Name: "db", Type: provider.BindingPostgres, Postgres: &provider.PostgresSpec{}},
			{Name: "uploads", Type: provider.BindingBucket, Bucket: &provider.BucketSpec{}},
		},
		App: &provider.AppSpec{
			App:       "api",
			Framework: "next",
			Functions: []provider.FunctionSpec{{Name: "fn--api--users"}},
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
	for i, patch := range overTheWire(patches) {
		results[i] = transformkit.Result{Patches: patch}
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

func offered(t *testing.T, spec provider.StackSpec) (*fakePass, []string) {
	t.Helper()
	pass := &fakePass{}
	if _, err := transformStackSpec(context.Background(), pass, spec); err != nil {
		t.Fatalf("transformStackSpec() = %v", err)
	}
	var seen []string
	for _, resource := range pass.seen.Resources {
		seen = append(seen, resource.Type+":"+resource.Name)
	}
	return pass, seen
}

func TestAnAppStackOffersOnlyTheFunctionsItProvisions(t *testing.T) {
	pass, seen := offered(t, specUnderTransform())

	want := []string{"function:fn--api--users"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("the app stack offered %v, want %v — a patch on a resource this stack never constructs reaches nothing", seen, want)
	}
	if pass.seen.Provider != transformProvider {
		t.Errorf("the transform was told provider %q, want %q", pass.seen.Provider, transformProvider)
	}
	if pass.seen.Env != "production" || pass.seen.EnvClass != string(edge.ClassProduction) {
		t.Errorf("the transform was told env %q class %q, want the spec's own coordinate", pass.seen.Env, pass.seen.EnvClass)
	}
}

func TestASpecWithNoTransformIsLeftExactlyAsItWasGiven(t *testing.T) {
	transformed, err := transformStackSpec(context.Background(), nil, specUnderTransform())
	if err != nil {
		t.Fatalf("transformStackSpec() with no pass = %v", err)
	}
	if transformed != nil {
		t.Errorf("transformStackSpec() = %+v with no pass, want the planned arguments left alone", transformed)
	}
}

func TestAPatchLandsOnThePulumiResourceThatOcelConstructsForIt(t *testing.T) {
	pass := patchingPass{patch: func(patches []transformkit.Patches) {
		patches[len(patches)-1]["lambda"] = map[string]any{"memorySize": 2048}
	}}

	transformed, err := transformStackSpec(context.Background(), pass, specUnderTransform())
	if err != nil {
		t.Fatalf("transformStackSpec() = %v", err)
	}
	names := functionResourceNames("shop", specUnderTransform().Ref.Name, "fn--api--users")
	patch, claimed := transformed.patches[names["lambda"]]
	if !claimed {
		t.Fatalf("nothing was claimed for %q, want the lambda patch", names["lambda"])
	}
	if patch["memorySize"] != float64(2048) {
		t.Errorf("memorySize = %v, want 2048", patch["memorySize"])
	}
}

func TestAnInfraStackOffersTheResourcesItProvisionsAndNotTheOnesItIsHandled(t *testing.T) {
	spec := specUnderTransform()
	spec.App = nil
	spec.Kind = provider.StackInfra
	spec.Resources[0].Binding = "legacy-orders"

	_, seen := offered(t, spec)

	want := []string{"bucket:uploads"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("the infra stack offered %v, want %v — a bound resource is somebody else's to patch", seen, want)
	}
}

func TestAPatchOnAResourceThisProviderNeverConstructsIsRefused(t *testing.T) {
	spec := specUnderTransform()
	spec.App = nil
	spec.Kind = provider.StackInfra
	pass := patchingPass{patch: func(patches []transformkit.Patches) {
		patches[1]["queue"] = map[string]any{"fifo": true}
	}}

	_, err := transformStackSpec(context.Background(), pass, spec)
	if err == nil || !strings.Contains(err.Error(), "queue") {
		t.Fatalf("transformStackSpec() = %v, want the resource this provider never constructs refused by name", err)
	}
}

func TestABucketWithNoDeclaredOriginsCanStillBeGivenCORSByATransform(t *testing.T) {
	spec := specUnderTransform()
	spec.App = nil
	spec.Kind = provider.StackInfra
	pass := patchingPass{patch: func(patches []transformkit.Patches) {
		patches[1]["cors"] = map[string]any{"corsRules": []any{map[string]any{"allowedMethods": []any{"GET"}}}}
	}}

	transformed, err := transformStackSpec(context.Background(), pass, spec)
	if err != nil {
		t.Fatalf("transformStackSpec() = %v, want a transform allowed to add CORS to a bucket that declares no origins", err)
	}
	if !transformed.opensCORS("uploads") {
		t.Fatal("the bucket was not marked as opened by the patch, so the deploy would construct nothing for it")
	}
	names := bucketResourceNames("shop", "production", "uploads")
	if _, registered := transformed.patches[names["cors"]]; !registered {
		t.Error("nothing was registered for the bucket's cors resource")
	}
}

func TestATransformReadsABindingOutputThroughTheSpecsOwnBindings(t *testing.T) {
	bindings := &publishedReader{bindings: []provider.Binding{
		{Type: provider.BindingPostgres, Name: "legacy", Properties: map[string]string{"runtime": "nodejs22.x"}},
	}}
	spec := specUnderTransform()
	spec.Bindings = bindings

	transformed, err := transformStackSpec(context.Background(), filledFromBinding(customBindingType, "legacy", "runtime"), spec)
	if err != nil {
		t.Fatalf("transformStackSpec() = %v", err)
	}
	names := functionResourceNames("shop", spec.Ref.Name, "fn--api--users")
	if got := transformed.patches[names["lambda"]]["runtime"]; got != "nodejs22.x" {
		t.Errorf("the function runs %q, want the value the published binding records", got)
	}
	if len(bindings.asked) != 1 || bindings.asked[0] != "legacy" {
		t.Fatalf("the pass asked the spec's bindings for %v, want the one output the transform named", bindings.asked)
	}
}

func TestATransformReadingABindingThisSpecProvisionsIsRefused(t *testing.T) {
	spec := specUnderTransform()
	spec.Bindings = &publishedReader{}

	_, err := transformStackSpec(context.Background(), filledFromBinding("postgres", "db", "runtime"), spec)
	var provisioned *ProvisionedOutputError
	if !errors.As(err, &provisioned) {
		t.Fatalf("transformStackSpec() = %v, want it refused: this spec provisions \"db\" itself, so its outputs are not there to read", err)
	}
}

func TestATransformReadingABindingThisProjectNeverBoundIsRefused(t *testing.T) {
	spec := specUnderTransform()
	spec.Resources[1].Binding = "archive"
	spec.Bindings = &publishedReader{}

	_, err := transformStackSpec(context.Background(), filledFromBinding("postgres", "orders", "runtime"), spec)
	var unbound *UnboundOutputError
	if !errors.As(err, &unbound) {
		t.Fatalf("transformStackSpec() = %v, want an unbound-output refusal", err)
	}
	if !slices.Equal(unbound.Declared, []string{"bucket.uploads"}) {
		t.Errorf("the refusal lists %v as bound, want what the config actually binds", unbound.Declared)
	}
}

func TestATransformReadingAnUnpublishedRecordNamesWhatIsPublished(t *testing.T) {
	spec := specUnderTransform()
	spec.Bindings = &publishedReader{bindings: []provider.Binding{
		{Type: provider.BindingBucket, Name: "archive", Properties: map[string]string{"bucket": "archive-bucket"}},
	}}

	_, err := transformStackSpec(context.Background(), filledFromBinding(customBindingType, "absent", "runtime"), spec)
	var unpublished *UnpublishedOutputError
	if !errors.As(err, &unpublished) {
		t.Fatalf("transformStackSpec() = %v, want an unpublished-output refusal", err)
	}
	if !slices.Equal(unpublished.Properties, []string{"archive"}) {
		t.Errorf("the refusal lists %v as published, want what the spec's bindings actually publish", unpublished.Properties)
	}
}

func TestATransformReadsABoundResourceUnderTheNameItIsPublishedAs(t *testing.T) {
	spec := specUnderTransform()
	spec.Resources[0].Binding = "legacy-orders"
	spec.Bindings = &publishedReader{bindings: []provider.Binding{
		{Type: provider.BindingPostgres, Name: "legacy-orders", Properties: map[string]string{"runtime": "nodejs22.x"}},
	}}

	transformed, err := transformStackSpec(context.Background(), filledFromBinding("postgres", "db", "runtime"), spec)
	if err != nil {
		t.Fatalf("transformStackSpec() = %v, want the bound resource's own published record read", err)
	}
	names := functionResourceNames("shop", spec.Ref.Name, "fn--api--users")
	if got := transformed.patches[names["lambda"]]["runtime"]; got != "nodejs22.x" {
		t.Errorf("the function runs %q, want the value the bound record has", got)
	}
}

func TestAStoreThatFailsToResolveABindingIsNotReportedAsABadProperty(t *testing.T) {
	torn := errors.New("the record's pair is torn")
	spec := specUnderTransform()
	spec.Bindings = &publishedReader{
		bindings: []provider.Binding{{Type: provider.BindingPostgres, Name: "legacy"}},
		failure:  torn,
	}

	_, err := transformStackSpec(context.Background(), filledFromBinding(customBindingType, "legacy", "runtime"), spec)
	if !errors.Is(err, torn) {
		t.Fatalf("transformStackSpec() = %v, want the store's own failure returned", err)
	}
	var property *OutputPropertyError
	if errors.As(err, &property) {
		t.Error("a store that could not be read was reported as a record missing a property")
	}
}

func TestABindingWithNoSuchPropertyNamesWhatItDoesHave(t *testing.T) {
	spec := specUnderTransform()
	spec.Bindings = &publishedReader{bindings: []provider.Binding{{
		Type:       provider.BindingPostgres,
		Name:       "legacy",
		Properties: map[string]string{"host": "db.internal", "port": "5432"},
	}}}

	_, err := transformStackSpec(context.Background(), filledFromBinding(customBindingType, "legacy", "runtime"), spec)
	var property *OutputPropertyError
	if !errors.As(err, &property) {
		t.Fatalf("transformStackSpec() = %v, want an OutputPropertyError", err)
	}
	if want := []string{"host", "port"}; !slices.Equal(property.Properties, want) {
		t.Errorf("Properties = %v, want the published record's own keys %v", property.Properties, want)
	}
}

func TestEveryOutputOffTheSameBindingResolvesItOnce(t *testing.T) {
	bindings := &publishedReader{bindings: []provider.Binding{
		{Type: provider.BindingPostgres, Name: "legacy", Properties: map[string]string{"runtime": "nodejs22.x"}},
	}}
	spec := specUnderTransform()
	spec.Bindings = bindings

	pass := patchingPass{patch: func(patches []transformkit.Patches) {
		placeholder := placeholderFor(customBindingType, "legacy", "runtime")
		patches[len(patches)-1]["lambda"] = map[string]any{
			"runtime":     placeholder,
			"description": placeholder,
		}
	}}

	if _, err := transformStackSpec(context.Background(), pass, spec); err != nil {
		t.Fatalf("transformStackSpec() = %v", err)
	}
	if len(bindings.asked) != 1 {
		t.Errorf("the pass resolved %v, want one read for the one binding both outputs name", bindings.asked)
	}
}
