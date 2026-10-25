package gcp

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	runv2 "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
	"github.com/ocelhq/ocel/platform/gcp/provider/pin"
)

func desiredOf(t *testing.T, s run.Serving) *runv2.GoogleCloudRunV2Service {
	t.Helper()
	desired, err := run.ServiceOf(s)
	if err != nil {
		t.Fatalf("serviceOf(%s) = %v", s.Service, err)
	}
	return desired
}

func TestAServerlessRevisionScalesToNothingAndIsBilledPerRequest(t *testing.T) {
	desired := desiredOf(t, run.Serving{
		Service: "ocel-shop-prod-web",
		Tier:    environment.TierProduction,
		Image:   "europe-west1-docker.pkg.dev/acme/ocel/web@sha256:abc",
		Account: "ocel-1a2b3c4d5e@acme.iam.gserviceaccount.com",
		Compute: provider.ComputeServerless,
	})

	template := desired.Template
	if template.Scaling.MinInstanceCount != 0 {
		t.Errorf("a function keeps %d instances warm, want none: it is billed per request", template.Scaling.MinInstanceCount)
	}
	container := template.Containers[0]
	if !container.Resources.CpuIdle {
		t.Error("a function keeps its cpu between requests, want it idle: that is what request-based billing is")
	}
	if container.StartupProbe != nil {
		t.Error("a function has a startup probe, and nothing declared a path to probe")
	}
	if got := container.Resources.Limits; got["cpu"] != "1" || got["memory"] != "512Mi" {
		t.Errorf("a revision asks for %v, want one vCPU and 512Mi", got)
	}
	if len(container.Ports) != 1 || container.Ports[0].ContainerPort != containerimage.Port {
		t.Errorf("a revision listens on %v, want %d, which is the port every image ocel builds binds",
			container.Ports, containerimage.Port)
	}
	if template.ServiceAccount != "ocel-1a2b3c4d5e@acme.iam.gserviceaccount.com" {
		t.Errorf("a revision runs as %q, want the app's own account it was handed", template.ServiceAccount)
	}
}

func TestAContainerRevisionKeepsAnInstanceUpAndIsProbedOnItsOwnPath(t *testing.T) {
	desired := desiredOf(t, run.Serving{
		Service:   "ocel-shop-prod-api",
		Image:     "europe-west1-docker.pkg.dev/acme/ocel/api@sha256:abc",
		Compute:   provider.ComputeContainer,
		Health:    "/healthz",
		Instances: provider.Instances{Min: 1, Max: 1},
	})

	template := desired.Template
	if template.Scaling.MinInstanceCount != 1 {
		t.Errorf("a container keeps %d instances up, want the one its app names", template.Scaling.MinInstanceCount)
	}
	container := template.Containers[0]
	if container.Resources.CpuIdle {
		t.Error("a container's cpu idles between requests, and a container is paid for by the instance: it keeps its cpu")
	}
	if container.StartupProbe == nil || container.StartupProbe.HttpGet == nil {
		t.Fatal("a container has no startup probe, and up means a 2xx on the path the deploy request named")
	}
	if got := container.StartupProbe.HttpGet; got.Path != "/healthz" || got.Port != containerimage.Port {
		t.Errorf("a container is probed at %v, want /healthz on %d", got, containerimage.Port)
	}
}

func TestARevisionSetsEveryValueTheDeployResolvedInOneOrder(t *testing.T) {
	desired := desiredOf(t, run.Serving{
		Service: "ocel-shop-prod-web",
		Tier:    environment.TierProduction,
		Image:   "web@sha256:abc",
		Compute: provider.ComputeServerless,
		Env:     map[string]string{"DATABASE_URL": "postgres://", "API_KEY": "k"},
	})

	var entries []string
	for _, entry := range desired.Template.Containers[0].Env {
		entries = append(entries, entry.Name+"="+entry.Value)
	}
	if got := strings.Join(entries, " "); got != "API_KEY=k DATABASE_URL=postgres://" {
		t.Errorf("a revision sets %q, want every value in one order, so an unchanged deploy asks for no new revision", got)
	}
}

func TestAFunctionAsksForTheMemoryAndTheTimeoutItsSpecNamed(t *testing.T) {
	desired := desiredOf(t, run.Serving{
		Service: "ocel-shop-prod-fn",
		Image:   "fn@sha256:abc",
		Compute: provider.ComputeServerless,
		Memory:  1024,
		Timeout: 90 * time.Second,
	})

	if got := desired.Template.Containers[0].Resources.Limits["memory"]; got != "1024Mi" {
		t.Errorf("a revision asks for %q of memory, want the 1024Mi its spec named", got)
	}
	if got := desired.Template.Timeout; got != "90s" {
		t.Errorf("a request is cut off after %q, want the 90s its spec named: a function that outruns its own timeout is killed by whatever this provider chose instead", got)
	}
}

func TestAFunctionThatNamesNoMemoryOrTimeoutKeepsTheProfileTheTierRunsOn(t *testing.T) {
	desired := desiredOf(t, run.Serving{
		Service: "ocel-shop-prod-fn",
		Image:   "fn@sha256:abc",
		Compute: provider.ComputeServerless,
	})

	if got := desired.Template.Containers[0].Resources.Limits["memory"]; got != run.RevisionMemory {
		t.Errorf("a revision that named no memory asks for %q, want the profile's %q", got, run.RevisionMemory)
	}
	if got := desired.Template.Timeout; got != "" {
		t.Errorf("a revision that named no timeout asks for %q, want Cloud Run's own", got)
	}
}

func TestATimeoutLongerThanARequestMayRunIsRefused(t *testing.T) {
	_, err := run.ServiceOf(run.Serving{
		Service: "ocel-shop-prod-fn",
		Image:   "fn@sha256:abc",
		Compute: provider.ComputeServerless,
		Timeout: run.MaxRequestTimeout + time.Second,
	})

	if err == nil {
		t.Fatal("serviceOf() asked for a timeout longer than Cloud Run allows, and Cloud Run would refuse the release with its own words")
	}
	if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeInvalid {
		t.Errorf("serviceOf() code = %v, want %v", code, refusal.CodeInvalid)
	}
	if !strings.Contains(err.Error(), "ocel-shop-prod-fn") {
		t.Errorf("serviceOf() = %v, want the service that asked for it named", err)
	}
}

func TestTrafficIsPinnedToOneRevisionByName(t *testing.T) {
	pinned := run.TrafficTo("ocel-shop-prod-web-00007-abc", nil)

	if len(pinned) != 1 {
		t.Fatalf("traffic is split %d ways, want all of it on the revision this release created", len(pinned))
	}
	if pinned[0].Type != run.TrafficByRevision {
		t.Errorf("traffic is allocated by %q, want %q: the latest revision is whatever ran last, which is not what this release promised",
			pinned[0].Type, run.TrafficByRevision)
	}
	if pinned[0].Revision != "ocel-shop-prod-web-00007-abc" || pinned[0].Percent != 100 {
		t.Errorf("traffic = %+v, want all of it on the named revision", pinned[0])
	}
}

func released(t *testing.T, server *googletest.Run, s run.Serving) (string, string) {
	t.Helper()
	if s.Image == "" {
		s.Image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:abc"
	}
	deployed, err := providerServing(server, t).services().DeployService(context.Background(), s, nil)
	if err != nil {
		t.Fatalf("deployService(%s) = %v", s.Service, err)
	}
	return deployed.Url, deployed.Revision
}

func TestAReleaseReportsTheRevisionItCreatedSoAPromotionCanPinIt(t *testing.T) {
	server := &googletest.Run{}
	first := serves("ocel-shop-prod-app")
	_, one := released(t, server, first)

	second := first
	second.Image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	_, two := released(t, server, second)

	if one == "" || two == "" {
		t.Fatalf("a release created revisions %q and %q, want each named: a promotion pins the revision its build recorded", one, two)
	}
	if one == two {
		t.Errorf("both releases created revision %q, want a revision apiece: a rollback to the first would pin what the second serves", one)
	}
}

func TestAReleaseOntoADeployedServiceLeavesItsTrafficOnTheRevisionItServed(t *testing.T) {
	server := &googletest.Run{}
	first := serves("ocel-shop-prod-app")
	_, one := released(t, server, first)
	if _, err := providerServing(server, t).services().Pin(context.Background(), first.Service, one, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", one, err)
	}

	second := first
	second.Image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	released(t, server, second)

	if service := server.Serving(); !run.ServedBy(service.Traffic, one) {
		t.Errorf("the service serves %+v after the second release, want all of it still on %s: only a promotion moves traffic, and the ledger has promoted nothing new", service.Traffic, one)
	}
}

func TestAPinWhosePromotionWasDisplacedWhileTheServiceMovedPinsNothing(t *testing.T) {
	server := &googletest.Run{}
	first := serves("ocel-shop-prod-app")
	_, one := released(t, server, first)
	p := providerServing(server, t)
	if _, err := p.services().Pin(context.Background(), first.Service, one, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", one, err)
	}
	second := first
	second.Image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	_, two := released(t, server, second)
	third := first
	third.Image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:three"
	_, three := released(t, server, third)

	displaced := errors.New("another promotion displaced this one")
	raced := false
	stillActive := func(ctx context.Context) error {
		if raced {
			return displaced
		}
		raced = true
		if _, err := p.services().Pin(ctx, first.Service, three, nil); err != nil {
			t.Fatalf("the racing Pin(%s) = %v", three, err)
		}
		return nil
	}
	if _, err := p.services().Pin(context.Background(), first.Service, two, stillActive); !errors.Is(err, displaced) {
		t.Fatalf("Pin(%s) raced by a pin that landed after its promotion was checked = %v, want that displacement", two, err)
	}
	if service := server.Serving(); !run.ServedBy(service.Traffic, three) {
		t.Errorf("the service serves %+v, want all of it on %s, the pin that raced it: a pin writes on the etag it read, so a pin that landed after that read makes it read again and ask again whether its promotion is still active", service.Traffic, three)
	}
}

func TestPinningTrafficKeepsEveryTagTheServiceCarries(t *testing.T) {
	server := &googletest.Run{}
	first := serves("ocel-shop-prod-app")
	_, one := released(t, server, first)
	second := first
	second.Image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	_, two := released(t, server, second)
	server.Service.Traffic = []*runv2.GoogleCloudRunV2TrafficTarget{
		{Type: run.TrafficByRevision, Revision: one, Percent: 100},
		{Type: run.TrafficByRevision, Revision: one, Tag: "r00000001"},
		{Type: run.TrafficByRevision, Revision: two, Tag: "r00000002"},
	}

	if _, err := providerServing(server, t).services().Pin(context.Background(), first.Service, two, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", two, err)
	}

	traffic := server.Serving().Traffic
	if !run.ServedBy(traffic, two) {
		t.Errorf("the service serves %+v, want all of it on %s", traffic, two)
	}
	for tag, revision := range map[string]string{"r00000001": one, "r00000002": two} {
		if !slices.ContainsFunc(traffic, func(target *runv2.GoogleCloudRunV2TrafficTarget) bool {
			return target.Tag == tag && run.RevisionName(target.Revision) == revision
		}) {
			t.Errorf("the service carries %+v after the pin, want tag %s still on %s: a tag is how a deployment reaches its own revision, and a promotion must not take it away",
				traffic, tag, revision)
		}
	}
}

func TestTheTagOfARevisionIsReadFromTheServiceAndAnUntaggedRevisionIsRefused(t *testing.T) {
	server := &googletest.Run{}
	tagged := serves("ocel-shop-prod-app")
	tagged.Tag = "r1a2b3c4d"
	_, one := released(t, server, tagged)
	untagged := serves("ocel-shop-prod-app")
	untagged.Image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	_, two := released(t, server, untagged)
	p := providerServing(server, t)

	if tag, err := p.services().ReadTag(context.Background(), tagged.Service, one); err != nil || tag != "r1a2b3c4d" {
		t.Errorf("ReadTag(%s) = %q, %v, want r1a2b3c4d", one, tag, err)
	}
	var refused refusal.Refusal
	if _, err := p.services().ReadTag(context.Background(), tagged.Service, two); !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Errorf("ReadTag(%s) of a revision no release tagged = %v, want a %s refusal", two, err, refusal.CodeNotReady)
	}
}

func TestUntaggingTakesTheTagOffItsRevisionAndLeavesTheTrafficWhereItWas(t *testing.T) {
	server := &googletest.Run{}
	first := serves("ocel-shop-prod-app")
	first.Tag = "r00000001"
	_, one := released(t, server, first)
	p := providerServing(server, t)
	if _, err := p.services().Pin(context.Background(), first.Service, one, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", one, err)
	}

	for range 2 {
		if err := p.services().Untag(context.Background(), first.Service, "r00000001"); err != nil {
			t.Fatalf("Untag() = %v", err)
		}
	}

	traffic := server.Serving().Traffic
	if slices.ContainsFunc(traffic, func(target *runv2.GoogleCloudRunV2TrafficTarget) bool { return target.Tag == "r00000001" }) {
		t.Errorf("the service carries %+v after the untag, want tag r00000001 gone", traffic)
	}
	if !run.ServedBy(traffic, one) {
		t.Errorf("the service serves %+v after the untag, want all of it still on %s", traffic, one)
	}
}

func TestWhatAServicePinsIsTheRevisionTakingAllOfItsTraffic(t *testing.T) {
	server := &googletest.Run{}
	first := serves("ocel-shop-prod-app")
	_, one := released(t, server, first)
	p := providerServing(server, t)
	ctx := context.Background()

	if pinned, err := p.services().ReadServing(ctx, first.Service); err != nil || pinned != one {
		t.Errorf("ReadServing() of a service following its latest revision = %q, %v, want %s, the revision it serves", pinned, err, one)
	}
	second := first
	second.Image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	_, two := released(t, server, second)
	if _, err := p.services().Pin(ctx, first.Service, two, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", two, err)
	}
	if pinned, err := p.services().ReadServing(ctx, first.Service); err != nil || pinned != two {
		t.Errorf("ReadServing() = %q, %v, want %s", pinned, err, two)
	}
}

func TestAReleaseOntoAServiceThatServesItsLatestRevisionKeepsServingThatRevision(t *testing.T) {
	server := &googletest.Run{}
	first := serves("ocel-shop-prod-app")
	_, one := released(t, server, first)

	second := first
	second.Image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	released(t, server, second)

	if service := server.Serving(); !run.ServedBy(service.Traffic, one) {
		t.Errorf("the service serves %+v after the second release, want all of it on %s, the revision it served: a service that follows its latest revision hands traffic to whatever a release creates", service.Traffic, one)
	}
}

func TestAFunctionTheSpecGivesNoURLOfItsOwnIsLeftPrivate(t *testing.T) {
	server := &googletest.Run{}

	released(t, server, run.Serving{Tier: environment.TierProduction, Service: "ocel-shop-prod-fn", Compute: provider.ComputeServerless})

	if desired := server.Current(); desired.InvokerIamDisabled {
		t.Error("a function the plan says has no url may be invoked without an invoker check, " +
			"and a function reached only through its app is reachable by everybody instead")
	}
}

func TestAFunctionTheSpecGivesAURLIsReachedWithoutAnAllUsersGrant(t *testing.T) {
	server := &googletest.Run{}

	released(t, server, run.Serving{Tier: environment.TierProduction, Service: "ocel-shop-prod-fn", Compute: provider.ComputeServerless, Public: true})

	if desired := server.Current(); !desired.InvokerIamDisabled {
		t.Error("a function the plan gives a url of its own still checks its invoker, and a service nobody may invoke answers nothing")
	}
	if bound := server.Bound(); len(bound) != 0 {
		t.Errorf("the release wrote %d iam policies, want none: an org that restricts iam members to its own domain refuses an "+
			"allUsers binding outright, and the service is opened by turning the invoker check off instead", len(bound))
	}
}

func TestAContainerAppIsAlwaysOpenedToThePublic(t *testing.T) {
	server := &googletest.Run{}

	released(t, server, run.Serving{Tier: environment.TierProduction, Service: "ocel-shop-prod-app", Compute: provider.ComputeContainer, Health: "/", Public: true})

	if desired := server.Current(); !desired.InvokerIamDisabled {
		t.Error("the app's own front still checks its invoker, and nothing else sits between the internet and it")
	}
}

func TestAReleaseOntoAPinnedServiceTagsTheRevisionItCreated(t *testing.T) {
	server := &googletest.Run{}
	first := serves("ocel-shop-prod-app")
	first.Tag = "r00000001"
	_, one := released(t, server, first)
	if _, err := providerServing(server, t).services().Pin(context.Background(), first.Service, one, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", one, err)
	}

	second := first
	second.Image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	second.Tag = "r00000002"
	_, two := released(t, server, second)

	if two == one {
		t.Fatalf("the second release reported revision %s, the one the service serves, want the revision it created: Cloud Run reports a revision ready only once it serves traffic", two)
	}
	for _, target := range server.Serving().Traffic {
		if target.Tag == second.Tag && run.RevisionName(target.Revision) != two {
			t.Errorf("tag %s is on %s, want %s: the edge reaches the release on its tag, and would serve what ran before", second.Tag, target.Revision, two)
		}
	}
}

func TestAReleaseOntoADeployedServiceNeverHandsTrafficBackToTheLatestRevision(t *testing.T) {
	server := &googletest.Run{}
	first := run.Serving{
		Service: "ocel-shop-prod-app",
		Tier:    environment.TierProduction,
		Image:   "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:one",
		Compute: provider.ComputeContainer,
		Health:  "/",
		Public:  true,
	}
	released(t, server, first)

	second := first
	second.Image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	released(t, server, second)

	for _, patched := range server.Releases() {
		if len(patched.Traffic) == 0 {
			t.Fatal("a patch named no traffic, and Cloud Run replaces the body it is handed: " +
				"the allocation this release pinned would go back to the latest revision, which is whatever ran last")
		}
		if patched.Traffic[0].Type != run.TrafficByRevision || patched.Traffic[0].Revision == "" {
			t.Errorf("a patch allocated traffic by %q, want it by the name of a revision", patched.Traffic[0].Type)
		}
	}
}

func TestPinningTrafficLeavesWhoMayReachTheServiceAsTheReleaseSetIt(t *testing.T) {
	server := &googletest.Run{}
	behind := run.Serving{
		Service: "ocel-shop-prod-web",
		Tier:    environment.TierProduction,
		Compute: provider.ComputeServerless,
		Public:  true,
		Ingress: run.IngressLoadBalancer,
	}
	released(t, server, behind)
	behind.Image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	released(t, server, behind)

	service := server.Serving()
	if service.Ingress != run.IngressLoadBalancer {
		t.Errorf("the service takes %q after its traffic was pinned, want %q: a patch replaces what it is handed, so pinning traffic alone must say it changes the traffic alone",
			service.Ingress, run.IngressLoadBalancer)
	}
	if !service.InvokerIamDisabled {
		t.Error("the service checks its invoker again after its traffic was pinned, and every visitor to a public app would be refused")
	}
}

func serves(service string) run.Serving {
	return run.Serving{
		Service: service,
		Tier:    environment.TierProduction,
		Image:   "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:one",
		Compute: provider.ComputeContainer,
		Health:  "/",
		Public:  true,
	}
}

func TestAServiceChangedUnderAReleaseIsReadAgainAndPatchedAgain(t *testing.T) {
	server := &googletest.Run{}
	released(t, server, serves("ocel-shop-prod-app"))

	server.PatchConflicts = 1
	again := serves("ocel-shop-prod-app")
	again.Image = "europe-west1-docker.pkg.dev/acme/ocel/app:sha256-two"
	released(t, server, again)

	service := server.Serving()
	if got := service.Template.Containers[0].Image; !strings.HasSuffix(got, "sha256-two") {
		t.Errorf("the service runs %q, want the image the second release named: a 409 says the read the write was built on is stale", got)
	}
}

func TestAServiceThatKeepsChangingUnderAReleaseIsRefusedRatherThanRetriedForever(t *testing.T) {
	server := &googletest.Run{}
	released(t, server, serves("ocel-shop-prod-app"))

	server.PatchConflicts = math.MaxInt
	before := server.Tries()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := providerServing(server, t).services().DeployService(ctx, serves("ocel-shop-prod-app"), nil)
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deployService() over a service that never stops changing = %v, want the release refused before the run's deadline", err)
	}
	if !strings.Contains(err.Error(), "ocel-shop-prod-app") {
		t.Errorf("deployService() = %v, want the service it was releasing named", err)
	}
	if patched := server.Tries(); patched-before < 2 {
		t.Errorf("the release patched %d times, want the conflicting patch rebuilt and sent again", patched-before)
	}
}

func TestARunSaysWhichCloudRunServiceItCreatesReleasesAndDeletesAndInWhichRegion(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	progress := &fake.Log{}
	ctx := context.Background()

	if _, err := p.services().DeployService(ctx, serves("ocel-shop-prod-app"), progress); err != nil {
		t.Fatalf("deployService() = %v", err)
	}
	if _, err := p.services().DeployService(ctx, serves("ocel-shop-prod-app"), progress); err != nil {
		t.Fatalf("deployService() again = %v", err)
	}
	if err := p.services().TearDown(ctx, "ocel-shop-prod-app", progress); err != nil {
		t.Fatalf("tearDown() = %v", err)
	}

	want := []string{
		"INFO Creating Cloud Run service ocel-shop-prod-app",
		"INFO Releasing a new revision of ocel-shop-prod-app",
		"INFO Deleting Cloud Run service ocel-shop-prod-app",
	}
	if got := progress.Lines(); !slices.Equal(got, want) {
		t.Errorf("the run said %q, want %q", got, want)
	}
}

func TestAServiceNoPromotionPinsServesEachRevisionItsReleaseMakes(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	ctx := context.Background()
	first, again := serves("ocel-connector"), serves("ocel-connector")
	again.Image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"

	if _, err := p.services().DeployAndRoute(ctx, first, nil); err != nil {
		t.Fatalf("deployAndRoute() = %v", err)
	}
	ran, err := p.services().DeployAndRoute(ctx, again, nil)
	if err != nil {
		t.Fatalf("deployAndRoute() again = %v", err)
	}

	if served := run.ServedRevision(server.Serving()); served != ran.Revision || served == "" {
		t.Errorf("the service serves %q, want %q, the revision the second release made", served, ran.Revision)
	}
}

func promotable(service string) run.Serving {
	s := serves(service)
	s.OpensOnPromotion = true
	return s
}

func TestABrandNewAppServiceAnswersNobodyUntilItsFirstPromotion(t *testing.T) {
	server := &googletest.Run{}
	app := promotable("ocel-shop-prod-app")
	_, one := released(t, server, app)

	if server.Serving().InvokerIamDisabled {
		t.Fatal("a service its first release created answers anyone before a promotion pinned it, and the ledger names nothing it serves")
	}
	if _, err := providerServing(server, t).services().Pin(context.Background(), app.Service, one, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", one, err)
	}
	if !server.Serving().InvokerIamDisabled {
		t.Error("the service still checks its invoker once its first promotion pinned it, and every visitor to a public app would be refused")
	}

	second := app
	second.Image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	released(t, server, second)
	if !server.Serving().InvokerIamDisabled {
		t.Error("a later release closed the service its promotion opened, and every visitor would be refused until the next promotion")
	}
}

func TestClosingAServiceAnotherPromotionRepinnedSinceLeavesItOpen(t *testing.T) {
	server := &googletest.Run{}
	app := promotable("ocel-shop-prod-app")
	_, one := released(t, server, app)
	p := providerServing(server, t)
	if _, err := p.services().Pin(context.Background(), app.Service, one, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", one, err)
	}
	writes := len(server.Patched)
	repinned := errors.New("another promotion pinned this service since")

	err := p.services().Close(context.Background(), app.Service, func(context.Context) error { return repinned })

	if !errors.Is(err, repinned) {
		t.Fatalf("Close() = %v, want the error that says the promotion is no longer the one pinned", err)
	}
	if !server.Serving().InvokerIamDisabled {
		t.Error("the service was closed although another promotion pinned it since the check")
	}
	if got := len(server.Patched); got != writes {
		t.Errorf("%d patches landed after the Close, want none", got-writes)
	}
}

func TestClosingAPromotedServiceTurnsItsInvokerCheckBackOn(t *testing.T) {
	server := &googletest.Run{}
	app := promotable("ocel-shop-prod-app")
	_, one := released(t, server, app)
	p := providerServing(server, t)
	if _, err := p.services().Pin(context.Background(), app.Service, one, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", one, err)
	}

	for range 2 {
		if err := p.services().Close(context.Background(), app.Service, nil); err != nil {
			t.Fatalf("Close() = %v", err)
		}
	}

	if server.Serving().InvokerIamDisabled {
		t.Error("a closed service answers anyone, and the pointer that promoted it was taken back")
	}
	if !run.ServedBy(server.Serving().Traffic, one) {
		t.Errorf("closing the service moved its traffic to %+v", server.Serving().Traffic)
	}
}

func TestClosingAnOpenServiceNoPromotionOpenedTurnsItsInvokerCheckBackOn(t *testing.T) {
	server := &googletest.Run{}
	app := serves("ocel-shop-prod-app")
	app.Public = true
	_, one := released(t, server, app)
	p := providerServing(server, t)
	if _, err := p.services().Pin(context.Background(), app.Service, one, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", one, err)
	}

	if err := p.services().Close(context.Background(), app.Service, nil); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	if server.Serving().InvokerIamDisabled {
		t.Error("a closed service answers anyone because it carries no label saying a promotion opened it, and the router says a removed pointer is served no more")
	}
}

func TestPinReportsWhetherItOpenedTheService(t *testing.T) {
	server := &googletest.Run{}
	app := promotable("ocel-shop-prod-app")
	_, one := released(t, server, app)
	p := providerServing(server, t)

	opened, err := p.services().Pin(context.Background(), app.Service, one, nil)
	if err != nil || !opened {
		t.Errorf("the first Pin(%s) = %v, %v, want it to say it opened the service: a promotion that fails later has to close it again", one, opened, err)
	}
	opened, err = p.services().Pin(context.Background(), app.Service, one, nil)
	if err != nil || opened {
		t.Errorf("Pin(%s) of an open service = %v, %v, want it to say it opened nothing", one, opened, err)
	}
}

func TestAPinWhoseOperationFailsStillReportsItOpenedTheService(t *testing.T) {
	server := &googletest.Run{FailRouting: true}
	app := promotable("ocel-shop-prod-app")
	_, one := released(t, server, app)
	p := providerServing(server, t)

	opened, err := p.services().Pin(context.Background(), app.Service, one, nil)

	if err == nil {
		t.Fatal("Pin = nil, want the failed operation")
	}
	if !opened {
		t.Error("Pin reports it opened nothing, and its patch opening the service was sent before the operation failed: a promotion putting services back would leave it open")
	}
}

func TestPinningBackWithoutOpeningLeavesAClosedServiceClosed(t *testing.T) {
	server := &googletest.Run{}
	app := promotable("ocel-shop-prod-app")
	_, one := released(t, server, app)
	second := app
	second.Image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	released(t, server, second)
	p := providerServing(server, t)

	if err := p.services().Restore(context.Background(), app.Service, one, nil); err != nil {
		t.Fatalf("Restore(%s) = %v", one, err)
	}

	if server.Serving().InvokerIamDisabled {
		t.Error("pinning a closed service back opened it to anyone, and a promotion putting a service back closes it first so its old revision answers nobody")
	}
	if !run.ServedBy(server.Serving().Traffic, one) {
		t.Errorf("the service serves %+v, want all of it on %s", server.Serving().Traffic, one)
	}
}

func TestARollbackIsNotRecordedOnTheRevisionTheServiceServes(t *testing.T) {
	ctx := context.Background()
	server := &googletest.Run{}
	app := promotable("ocel-shop-prod-app")
	_, one := released(t, server, app)
	p := providerServing(server, t)
	if _, err := p.services().Pin(ctx, app.Service, one, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", one, err)
	}

	err := p.services().RecordRollback(ctx, app.Service, one, pin.Rollback{Previous: "x"})

	if !errors.Is(err, pin.ErrRevisionServed) {
		t.Errorf("RecordRollback(%s) = %v, want %v: the service serves it", one, err, pin.ErrRevisionServed)
	}
	if _, found, err := p.services().ReadRollback(ctx, app.Service, one); err != nil || found {
		t.Errorf("ReadRollback(%s) = found %v, %v, want nothing recorded for the revision the service serves", one, found, err)
	}
}

func TestARecordedRollbackIsReadBackUntilARevisionIsPinnedAgain(t *testing.T) {
	ctx := context.Background()
	server := &googletest.Run{}
	app := promotable("ocel-shop-prod-app")
	_, one := released(t, server, app)
	second := app
	second.Image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	_, two := released(t, server, second)
	p := providerServing(server, t)

	recorded := pin.Rollback{Previous: one, Opened: true}
	if err := p.services().RecordRollback(ctx, app.Service, two, recorded); err != nil {
		t.Fatalf("RecordRollback(%s) = %v", two, err)
	}
	third := app
	third.Image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:three"
	released(t, server, third)
	if got, found, err := p.services().ReadRollback(ctx, app.Service, two); err != nil || !found || got != recorded {
		t.Fatalf("ReadRollback(%s) after another release = %+v, %v, %v, want %+v: a release keeps what rollbacks the service records", two, got, found, err, recorded)
	}
	if _, found, err := p.services().ReadRollback(ctx, app.Service, one); err != nil || found {
		t.Errorf("ReadRollback(%s) = found %v, %v, want nothing recorded for a revision no promotion rolled back", one, found, err)
	}

	if _, err := p.services().Pin(ctx, app.Service, two, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", two, err)
	}
	if _, found, err := p.services().ReadRollback(ctx, app.Service, two); err != nil || found {
		t.Errorf("ReadRollback(%s) after a promotion pinned it again = found %v, %v, want nothing: a promotion that lands on it makes it what the service serves",
			two, found, err)
	}
}

func TestARollbackRecordThatCannotBeDecodedIsReportedRatherThanReadAsNothing(t *testing.T) {
	ctx := context.Background()
	server := &googletest.Run{}
	app := promotable("ocel-shop-prod-app")
	_, one := released(t, server, app)
	second := app
	second.Image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	_, two := released(t, server, second)
	p := providerServing(server, t)
	server.Mu.Lock()
	server.Service.Annotations = map[string]string{run.RollbacksAnnotation: "{not json"}
	server.Mu.Unlock()

	if _, found, err := p.services().ReadRollback(ctx, app.Service, one); err == nil || found {
		t.Errorf("ReadRollback(%s) = found %v, %v, want an error: the record cannot be decoded", one, found, err)
	}

	if _, err := p.services().Pin(ctx, app.Service, two, nil); err != nil {
		t.Fatalf("Pin(%s) = %v, want a pin to replace a record it cannot decode", two, err)
	}
	server.Mu.Lock()
	recorded := server.Service.Annotations[run.RollbacksAnnotation]
	server.Mu.Unlock()
	if recorded != "" {
		t.Errorf("the service records %q after the pin, want the undecodable record gone", recorded)
	}
}

func TestAPinThatFindsTheServiceOpenedOnItsRetryDoesNotClaimToHaveOpenedIt(t *testing.T) {
	server := &googletest.Run{}
	app := promotable("ocel-shop-prod-app")
	_, one := released(t, server, app)
	server.PatchConflicts = 1
	server.OnConflict = func(s *runv2.GoogleCloudRunV2Service) { s.InvokerIamDisabled = true }

	opened, err := providerServing(server, t).services().Pin(context.Background(), app.Service, one, nil)
	if err != nil {
		t.Fatalf("Pin(%s) = %v", one, err)
	}
	if opened {
		t.Error("Pin says it opened a service another promotion opened while it retried, and a failed promotion would close what that one opened")
	}
}

func TestAPinWhoseRetryFailsBeforeItsPatchLandsDoesNotClaimToHaveOpenedTheService(t *testing.T) {
	server := &googletest.Run{}
	app := promotable("ocel-shop-prod-app")
	_, one := released(t, server, app)
	server.PatchConflicts = 1
	displaced := errors.New("another promotion displaced this one")
	checks := 0
	stillActive := func(context.Context) error {
		checks++
		if checks > 1 {
			return displaced
		}
		return nil
	}

	opened, err := providerServing(server, t).services().Pin(context.Background(), app.Service, one, stillActive)

	if !errors.Is(err, displaced) {
		t.Fatalf("Pin(%s) = %v, want the displacement its retry found", one, err)
	}
	if opened {
		t.Error("Pin says it opened the service although the patch that opens it conflicted and never landed, and a failed promotion would close what it never opened")
	}
}

func TestAPinThatKeepsConflictingUntilItGivesUpDoesNotClaimToHaveOpenedTheService(t *testing.T) {
	server := &googletest.Run{}
	app := promotable("ocel-shop-prod-app")
	_, one := released(t, server, app)
	server.PatchConflicts = math.MaxInt
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	opened, err := providerServing(server, t).services().Pin(ctx, app.Service, one, nil)

	if err == nil {
		t.Fatal("Pin = nil, want it to give up on the conflicts")
	}
	if opened {
		t.Error("Pin says it opened a service whose every patch conflicted, and a failed promotion would close it")
	}
}

func TestPinningAServiceNoPromotionOpensLeavesItsInvokerCheckOn(t *testing.T) {
	server := &googletest.Run{}
	worker := run.Serving{Tier: environment.TierProduction, Service: "ocel-shop-prod-worker", Image: "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:one", Compute: provider.ComputeServerless}
	_, one := released(t, server, worker)

	p := providerServing(server, t)
	if _, err := p.services().Pin(context.Background(), worker.Service, one, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", one, err)
	}
	if err := p.services().Close(context.Background(), worker.Service, nil); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	if server.Serving().InvokerIamDisabled {
		t.Error("pinning a service that only its invokers may reach opened it to anyone")
	}
}

func TestWarmingAPromotedRevisionAsksItsOwnUrlOnTheAppsHealthPath(t *testing.T) {
	asked := make(chan string, 4)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked <- r.Method + " " + r.URL.Path
	}))
	t.Cleanup(app.Close)
	server := &googletest.Run{}
	open := serves("ocel-shop-prod-app")
	_, one := released(t, server, open)
	server.Service.TrafficStatuses = []*runv2.GoogleCloudRunV2TrafficTargetStatus{{Type: run.TrafficByRevision, Revision: one, Tag: "r00000001", Uri: app.URL}}

	if err := providerServing(server, t).services().Warm(context.Background(), open.Service, one, "/healthz"); err != nil {
		t.Fatalf("Warm() = %v", err)
	}

	select {
	case got := <-asked:
		if got != "HEAD /healthz" {
			t.Errorf("warming asked %q, want HEAD /healthz", got)
		}
	default:
		t.Error("warming asked the revision nothing, and the first visitor after the promotion pays its cold start")
	}
}

func TestAWarmTheServiceTurnsAwayOrFailsToServeIsReportedAndAnAnswerFromTheAppIsNot(t *testing.T) {
	gateRefusal := map[string]string{"X-Robots-Tag": "noindex", "Cache-Control": "private, no-store"}
	for name, tc := range map[string]struct {
		status  int
		headers map[string]string
		wantErr bool
	}{
		"the proxy refusing the token":           {http.StatusForbidden, nil, true},
		"the proxy finding no token":             {http.StatusUnauthorized, nil, true},
		"an app failing to start":                {http.StatusServiceUnavailable, nil, true},
		"an app with nothing at its health path": {http.StatusNotFound, nil, false},
		"the gate asking the app's visitor in":   {http.StatusUnauthorized, gateRefusal, false},
		"a 401 that only claims noindex":         {http.StatusUnauthorized, map[string]string{"X-Robots-Tag": "noindex"}, true},
	} {
		t.Run(name, func(t *testing.T) {
			app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for header, value := range tc.headers {
					w.Header().Set(header, value)
				}
				w.WriteHeader(tc.status)
			}))
			t.Cleanup(app.Close)
			server := &googletest.Run{}
			open := serves("ocel-shop-prod-app")
			_, one := released(t, server, open)
			server.Service.TrafficStatuses = []*runv2.GoogleCloudRunV2TrafficTargetStatus{{Type: run.TrafficByRevision, Revision: one, Tag: "r00000001", Uri: app.URL}}

			err := providerServing(server, t).services().Warm(context.Background(), open.Service, one, "/healthz")

			if (err != nil) != tc.wantErr {
				t.Errorf("Warm() = %v against a %d answer, want an error: %v", err, tc.status, tc.wantErr)
			}
		})
	}
}

func TestARevisionOnlyTheLoadBalancerReachesIsNotAskedToWarm(t *testing.T) {
	asked := make(chan string, 4)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked <- r.Method + " " + r.URL.Path
	}))
	t.Cleanup(app.Close)
	server := &googletest.Run{}
	behind := serves("ocel-shop-prod-app")
	behind.Ingress = run.IngressLoadBalancer
	_, one := released(t, server, behind)
	server.Service.TrafficStatuses = []*runv2.GoogleCloudRunV2TrafficTargetStatus{{Type: run.TrafficByRevision, Revision: one, Tag: "r00000001", Uri: app.URL}}

	if err := providerServing(server, t).services().Warm(context.Background(), behind.Service, one, "/"); err != nil {
		t.Fatalf("Warm() = %v", err)
	}
	if len(asked) != 0 {
		t.Error("warming asked a revision whose ingress admits the load balancer alone, and the request could only be turned away")
	}
}

func TestAReleaseRetriedAfterAConflictKeepsOnlyTheRollbacksTheServiceRecordsOnTheRetry(t *testing.T) {
	ctx := context.Background()
	server := &googletest.Run{}
	app := promotable("ocel-shop-prod-app")
	_, one := released(t, server, app)
	p := providerServing(server, t)
	if _, err := p.services().Pin(ctx, app.Service, one, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", one, err)
	}
	if err := p.services().RecordRollback(ctx, app.Service, "rev-x", pin.Rollback{Previous: one}); err != nil {
		t.Fatalf("RecordRollback(rev-x) = %v", err)
	}
	server.PatchConflicts = 1
	server.OnConflict = func(s *runv2.GoogleCloudRunV2Service) { s.Annotations = nil }

	second := app
	second.Image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	released(t, server, second)

	if got := server.Serving().Annotations[run.RollbacksAnnotation]; got != "" {
		t.Errorf("the service records rollbacks %q after the retried release, want none: the retry read a service that recorded none", got)
	}
}

func TestAServerlessServiceBilledPerInstanceKeepsItsCPUBetweenRequests(t *testing.T) {
	idles := func(billing run.Billing) bool {
		desired := desiredOf(t, run.Serving{
			Service: "ocel-shop-pr-7-web",
			Image:   "europe-west1-docker.pkg.dev/acme/ocel/web@sha256:abc",
			Account: "ocel-1a2b3c4d5e@acme.iam.gserviceaccount.com",
			Compute: provider.ComputeServerless,
			Billing: billing,
		})
		return desired.Template.Containers[0].Resources.CpuIdle
	}

	if idles(run.BillingInstance) {
		t.Error("a serverless service billed per instance idles its cpu between requests, want it kept")
	}
	if !idles(run.BillingRequest) {
		t.Error("a serverless service billed per request keeps its cpu between requests, want it idle")
	}
}
