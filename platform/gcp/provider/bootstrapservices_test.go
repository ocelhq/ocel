package gcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"google.golang.org/api/cloudresourcemanager/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

type freshProject struct {
	usage    *googletest.ServiceUsage
	withheld []string

	mu    sync.Mutex
	calls []string
}

func (f *freshProject) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *freshProject) readsBeyondServiceUsage() []string {
	var reads []string
	for _, call := range f.called() {
		if !strings.Contains(call, "/services") && !strings.Contains(call, "/operations/") && !strings.HasSuffix(call, ":testIamPermissions") {
			reads = append(reads, call)
		}
	}
	return reads
}

func everyServiceOff() []string {
	b := composed(bootstrap.Engine{Clients: googletest.Clients("http://unused")})
	return b.TouchedAPIs(environment.TierProduction)
}

func (f *freshProject) serve(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		if f.usage.Serve(w, r) {
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/v1/projects/"+googletest.Project+":testIamPermissions" {
			if asked := googletest.ReadBody[cloudresourcemanager.TestIamPermissionsRequest](w, r); asked != nil {
				googletest.WriteBody(w, &cloudresourcemanager.TestIamPermissionsResponse{Permissions: slices.DeleteFunc(asked.Permissions, func(permission string) bool {
					return slices.Contains(f.withheld, permission)
				})})
			}
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"the fresh project serves nothing more"}}`))
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func openingFreshProject(t *testing.T, f *freshProject) bootstrap.Engine {
	t.Helper()
	return composed(bootstrap.Engine{
		Clients:       googletest.Clients(f.serve(t)),
		Records:       fake.NewKeyValues(),
		Surveyed:      &bootstrap.Surveys{},
		ServiceStates: &bootstrap.ServiceStates{},
		Principal:     func(context.Context) (string, error) { return "admin@acme.example", nil },
	})
}

var keysBootstrap = provider.BootstrapRequest{Tier: environment.TierProduction, Parts: []string{bootstrap.KeysPart}}

func switchedOnInPlan(plan provider.Plan) []string {
	var apis []string
	for _, group := range plan.Groups {
		for _, change := range group.Changes {
			if change.Kind == string(bootstrap.KindServiceUsage) {
				apis = append(apis, change.Name)
			}
		}
	}
	return apis
}

func TestABootstrapPlanOverAProjectWithEveryServiceOffReadsNoneOfThemAndShowsSwitchingOnOnlyThoseItNeeds(t *testing.T) {
	t.Parallel()
	f := &freshProject{usage: &googletest.ServiceUsage{ServicesOff: everyServiceOff()}}
	b := openingFreshProject(t, f)

	plan, err := b.Plan(t.Context(), keysBootstrap)
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}

	if want := append(slices.Clone(bootstrap.BootstrapAPIs), "cloudkms.googleapis.com"); !slices.Equal(switchedOnInPlan(plan), want) {
		t.Errorf("the plan switches on %v, want %v: only what the base and the keys part need", switchedOnInPlan(plan), want)
	}
	if reads := f.readsBeyondServiceUsage(); len(reads) > 0 {
		t.Errorf("Plan() read %v from services that are off", reads)
	}
	if f.usage.BatchGets != 1 || f.usage.Gets != 0 {
		t.Errorf("read the services in %d batches and %d single reads, want one batch", f.usage.BatchGets, f.usage.Gets)
	}
	if len(f.usage.Enabled) > 0 {
		t.Errorf("Plan() switched on %v, want nothing switched on before the plan is consented to", f.usage.Enabled)
	}
	for _, change := range plan.Groups[0].Changes {
		if change.Action == provider.ActionKeep && change.Reason == provider.ReasonCurrent {
			t.Errorf("the plan keeps %s %s as current, want it read as absent: its service is off", change.Kind, change.Name)
		}
	}
}

func TestABootstrapStatusOverAProjectWithEveryServiceOffReadsNoneOfThemAndFindsNoBootstrap(t *testing.T) {
	t.Parallel()
	f := &freshProject{usage: &googletest.ServiceUsage{ServicesOff: everyServiceOff()}}
	b := openingFreshProject(t, f)

	described, err := b.Describe(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatalf("Describe() = %v", err)
	}

	if described.Present {
		t.Error("Describe() found a bootstrap in a project whose services are all off")
	}
	if reads := f.readsBeyondServiceUsage(); len(reads) > 0 {
		t.Errorf("Describe() read %v from services that are off", reads)
	}
	if f.usage.BatchGets != 1 {
		t.Errorf("read the services in %d batches, want one", f.usage.BatchGets)
	}
}

func TestABootstrapSwitchesOnTheServicesItNeedsBeforeItReadsAnything(t *testing.T) {
	t.Parallel()
	f := &freshProject{usage: &googletest.ServiceUsage{ServicesOff: everyServiceOff()}}
	b := openingFreshProject(t, f)

	if err := b.Apply(t.Context(), keysBootstrap, nil); err == nil {
		t.Fatal("Apply() = nil, want the fresh project's first read refused")
	}

	want := append(slices.Clone(bootstrap.BootstrapAPIs), "cloudkms.googleapis.com")
	if !slices.Equal(f.usage.EnabledIDs(), want) {
		t.Errorf("Apply() switched on %v, want %v", f.usage.EnabledIDs(), want)
	}
	calls := f.called()
	enabled := slices.IndexFunc(calls, func(call string) bool { return strings.HasSuffix(call, ":batchEnable") })
	first := slices.Index(calls, f.readsBeyondServiceUsage()[0])
	if enabled < 0 || first < enabled {
		t.Errorf("Apply() called %v, want every service it needs switched on before its first read", calls)
	}
}

func TestABootstrapWhoseCredentialMayNotSwitchServicesOnIsRefusedOnceBeforeItReadsOrChangesAnything(t *testing.T) {
	t.Parallel()

	for name, run := range map[string]func(bootstrap.Engine) error{
		"plan":  func(b bootstrap.Engine) error { _, err := b.Plan(t.Context(), keysBootstrap); return err },
		"apply": func(b bootstrap.Engine) error { return b.Apply(t.Context(), keysBootstrap, nil) },
	} {
		f := &freshProject{usage: &googletest.ServiceUsage{ServicesOff: everyServiceOff()}, withheld: []string{"serviceusage.services.enable"}}

		err := run(openingFreshProject(t, f))

		var refused refusal.Refusal
		if !errors.As(err, &refused) || refused.Code != refusal.CodeDenied {
			t.Fatalf("%s = %v, want a %s refusal", name, err, refusal.CodeDenied)
		}
		for _, api := range append(slices.Clone(bootstrap.BootstrapAPIs), "cloudkms.googleapis.com") {
			if !strings.Contains(refused.Message, api) {
				t.Errorf("%s refused with %q, want it to name %s", name, refused.Message, api)
			}
		}
		if !strings.Contains(refused.Message, "admin@acme.example") || !strings.Contains(refused.Message, "gcloud services enable") || !strings.Contains(refused.Message, bootstrap.ServicesRole) {
			t.Errorf("%s refused with %q, want the principal, the gcloud command and the role", name, refused.Message)
		}
		if reads := f.readsBeyondServiceUsage(); len(reads) > 0 || len(f.usage.Enabled) > 0 {
			t.Errorf("%s read %v and switched on %v before refusing, want neither", name, reads, f.usage.Enabled)
		}
	}
}

func TestAPartAddedOnFirstUseSwitchesOnTheServicesItNeedsBeforeItMakesAnything(t *testing.T) {
	t.Parallel()
	server := googletest.GrantedIAM()
	server.ServicesOff = []string{"cloudkms.googleapis.com", "cloudscheduler.googleapis.com"}
	b := bootstrappingOver(t, server)

	if err := b.EnsurePart(t.Context(), environment.TierProduction, "shop", bootstrap.Keys.For(b, environment.TierProduction)); err != nil {
		t.Fatalf("EnsurePart(keys) = %v", err)
	}

	if got := server.EnabledIDs(); !slices.Equal(got, []string{"cloudkms.googleapis.com"}) {
		t.Errorf("adding the keys part switched on %v, want only cloudkms.googleapis.com", got)
	}
}

func TestTheRecordsOfAProjectWhoseFirestoreIsOffReadAsAbsentWithoutACall(t *testing.T) {
	t.Parallel()
	f := &freshProject{usage: &googletest.ServiceUsage{ServicesOff: everyServiceOff()}}
	p := pushing(t, f.serve(t))
	p.records = nil
	tier := environment.TierProduction
	if _, err := (bootstrapGate{p}).Describe(t.Context(), tier); err != nil {
		t.Fatalf("Describe() = %v", err)
	}

	_, listed := p.KeyValues().List(t.Context(), stackrecords.ProjectsPartition(tier))
	_, read := p.KeyValues().Read(t.Context(), stackrecords.ProjectsPartition(tier).Key("shop"))

	for name, err := range map[string]error{"List": listed, "Read": read} {
		var refused refusal.Refusal
		if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
			t.Errorf("%s() of a tier whose Firestore is off = %v, want the %s refusal of a tier with no records database", name, err, refusal.CodeNotReady)
		}
	}
	if reads := f.readsBeyondServiceUsage(); len(reads) > 0 {
		t.Errorf("the records read %v from a Firestore that is off", reads)
	}
}

func TestABootstrapPlanBehindAnEdgeThatRunsCodeReadsNoneOfItsAssetsWhileTheirServicesAreOff(t *testing.T) {
	t.Parallel()
	f := &freshProject{usage: &googletest.ServiceUsage{ServicesOff: everyServiceOff()}}
	b := openingFreshProject(t, f)
	b.Edges = &frontRegistry{front: &countingFront{runsCode: true}}

	plan, err := b.Plan(t.Context(), provider.BootstrapRequest{Tier: environment.TierProduction, Parts: []string{bootstrap.CloudflareOriginPart}})
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}

	if reads := f.readsBeyondServiceUsage(); len(reads) > 0 {
		t.Errorf("Plan() read %v from services that are off", reads)
	}
	for _, group := range plan.Groups {
		for _, change := range group.Changes {
			if change.Kind == string(bootstrap.KindSecret) && change.Name == b.Clients.OriginSecret(environment.TierProduction, cloudflare.Kind) && change.Action != provider.ActionCreate {
				t.Errorf("the plan shows the origin secret as %s, want a create: Secret Manager is off", change.Action)
			}
		}
	}
}
