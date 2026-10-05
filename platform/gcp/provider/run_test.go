package gcp

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/api/googleapi"
	run "google.golang.org/api/run/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/pin"
)

func desiredOf(t *testing.T, s serving) *run.GoogleCloudRunV2Service {
	t.Helper()
	desired, err := serviceOf(s)
	if err != nil {
		t.Fatalf("serviceOf(%s) = %v", s.service, err)
	}
	return desired
}

func TestAServerlessRevisionScalesToNothingAndIsBilledPerRequest(t *testing.T) {
	desired := desiredOf(t, serving{
		service: "ocel-shop-prod-web",
		image:   "europe-west1-docker.pkg.dev/acme/ocel/web@sha256:abc",
		account: "ocel-1a2b3c4d5e@acme.iam.gserviceaccount.com",
		compute: provider.ComputeServerless,
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
	desired := desiredOf(t, serving{
		service:   "ocel-shop-prod-api",
		image:     "europe-west1-docker.pkg.dev/acme/ocel/api@sha256:abc",
		compute:   provider.ComputeContainer,
		health:    "/healthz",
		instances: provider.Instances{Min: 1, Max: 1},
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
	desired := desiredOf(t, serving{
		service: "ocel-shop-prod-web",
		image:   "web@sha256:abc",
		compute: provider.ComputeServerless,
		env:     map[string]string{"DATABASE_URL": "postgres://", "API_KEY": "k"},
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
	desired := desiredOf(t, serving{
		service: "ocel-shop-prod-fn",
		image:   "fn@sha256:abc",
		compute: provider.ComputeServerless,
		memory:  1024,
		timeout: 90 * time.Second,
	})

	if got := desired.Template.Containers[0].Resources.Limits["memory"]; got != "1024Mi" {
		t.Errorf("a revision asks for %q of memory, want the 1024Mi its spec named", got)
	}
	if got := desired.Template.Timeout; got != "90s" {
		t.Errorf("a request is cut off after %q, want the 90s its spec named: a function that outruns its own timeout is killed by whatever this provider chose instead", got)
	}
}

func TestAFunctionThatNamesNoMemoryOrTimeoutKeepsTheProfileTheTierRunsOn(t *testing.T) {
	desired := desiredOf(t, serving{
		service: "ocel-shop-prod-fn",
		image:   "fn@sha256:abc",
		compute: provider.ComputeServerless,
	})

	if got := desired.Template.Containers[0].Resources.Limits["memory"]; got != revisionMemory {
		t.Errorf("a revision that named no memory asks for %q, want the profile's %q", got, revisionMemory)
	}
	if got := desired.Template.Timeout; got != "" {
		t.Errorf("a revision that named no timeout asks for %q, want Cloud Run's own", got)
	}
}

func TestATimeoutLongerThanARequestMayRunIsRefused(t *testing.T) {
	_, err := serviceOf(serving{
		service: "ocel-shop-prod-fn",
		image:   "fn@sha256:abc",
		compute: provider.ComputeServerless,
		timeout: maxRequestTimeout + time.Second,
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
	pinned := trafficTo("ocel-shop-prod-web-00007-abc", nil)

	if len(pinned) != 1 {
		t.Fatalf("traffic is split %d ways, want all of it on the revision this release created", len(pinned))
	}
	if pinned[0].Type != trafficByRevision {
		t.Errorf("traffic is allocated by %q, want %q: the latest revision is whatever ran last, which is not what this release promised",
			pinned[0].Type, trafficByRevision)
	}
	if pinned[0].Revision != "ocel-shop-prod-web-00007-abc" || pinned[0].Percent != 100 {
		t.Errorf("traffic = %+v, want all of it on the named revision", pinned[0])
	}
}

func released(t *testing.T, server *runServer, s serving) (string, string) {
	t.Helper()
	if s.image == "" {
		s.image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:abc"
	}
	deployed, err := server.open(t).deployService(context.Background(), s, nil)
	if err != nil {
		t.Fatalf("deployService(%s) = %v", s.service, err)
	}
	return deployed.url, deployed.revision
}

func TestAReleaseReportsTheRevisionItCreatedSoAPromotionCanPinIt(t *testing.T) {
	server := &runServer{}
	first := serves("ocel-shop-prod-app")
	_, one := released(t, server, first)

	second := first
	second.image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	_, two := released(t, server, second)

	if one == "" || two == "" {
		t.Fatalf("a release created revisions %q and %q, want each named: a promotion pins the revision its build recorded", one, two)
	}
	if one == two {
		t.Errorf("both releases created revision %q, want a revision apiece: a rollback to the first would pin what the second serves", one)
	}
}

func TestAReleaseOntoADeployedServiceLeavesItsTrafficOnTheRevisionItServed(t *testing.T) {
	server := &runServer{}
	first := serves("ocel-shop-prod-app")
	_, one := released(t, server, first)
	if _, err := server.open(t).Pin(context.Background(), first.service, one, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", one, err)
	}

	second := first
	second.image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	released(t, server, second)

	if service := server.serving(); !servedBy(service.Traffic, one) {
		t.Errorf("the service serves %+v after the second release, want all of it still on %s: only a promotion moves traffic, and the ledger has promoted nothing new", service.Traffic, one)
	}
}

func TestAPinWhosePromotionWasDisplacedWhileTheServiceMovedPinsNothing(t *testing.T) {
	server := &runServer{}
	first := serves("ocel-shop-prod-app")
	_, one := released(t, server, first)
	p := server.open(t)
	if _, err := p.Pin(context.Background(), first.service, one, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", one, err)
	}
	second := first
	second.image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	_, two := released(t, server, second)
	third := first
	third.image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:three"
	_, three := released(t, server, third)

	displaced := errors.New("another promotion displaced this one")
	raced := false
	stillActive := func(ctx context.Context) error {
		if raced {
			return displaced
		}
		raced = true
		if _, err := p.Pin(ctx, first.service, three, nil); err != nil {
			t.Fatalf("the racing Pin(%s) = %v", three, err)
		}
		return nil
	}
	if _, err := p.Pin(context.Background(), first.service, two, stillActive); !errors.Is(err, displaced) {
		t.Fatalf("Pin(%s) raced by a pin that landed after its promotion was checked = %v, want that displacement", two, err)
	}
	if service := server.serving(); !servedBy(service.Traffic, three) {
		t.Errorf("the service serves %+v, want all of it on %s, the pin that raced it: a pin writes on the etag it read, so a pin that landed after that read makes it read again and ask again whether its promotion is still active", service.Traffic, three)
	}
}

func TestPinningTrafficKeepsEveryTagTheServiceCarries(t *testing.T) {
	server := &runServer{}
	first := serves("ocel-shop-prod-app")
	_, one := released(t, server, first)
	second := first
	second.image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	_, two := released(t, server, second)
	server.service.Traffic = []*run.GoogleCloudRunV2TrafficTarget{
		{Type: trafficByRevision, Revision: one, Percent: 100},
		{Type: trafficByRevision, Revision: one, Tag: "r00000001"},
		{Type: trafficByRevision, Revision: two, Tag: "r00000002"},
	}

	if _, err := server.open(t).Pin(context.Background(), first.service, two, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", two, err)
	}

	traffic := server.serving().Traffic
	if !servedBy(traffic, two) {
		t.Errorf("the service serves %+v, want all of it on %s", traffic, two)
	}
	for tag, revision := range map[string]string{"r00000001": one, "r00000002": two} {
		if !slices.ContainsFunc(traffic, func(target *run.GoogleCloudRunV2TrafficTarget) bool {
			return target.Tag == tag && revisionName(target.Revision) == revision
		}) {
			t.Errorf("the service carries %+v after the pin, want tag %s still on %s: a tag is how a deployment reaches its own revision, and a promotion must not take it away",
				traffic, tag, revision)
		}
	}
}

func TestTheTagOfARevisionIsReadFromTheServiceAndAnUntaggedRevisionIsRefused(t *testing.T) {
	server := &runServer{}
	tagged := serves("ocel-shop-prod-app")
	tagged.tag = "r1a2b3c4d"
	_, one := released(t, server, tagged)
	untagged := serves("ocel-shop-prod-app")
	untagged.image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	_, two := released(t, server, untagged)
	p := server.open(t)

	if tag, err := p.ReadTag(context.Background(), tagged.service, one); err != nil || tag != "r1a2b3c4d" {
		t.Errorf("ReadTag(%s) = %q, %v, want r1a2b3c4d", one, tag, err)
	}
	var refused refusal.Refusal
	if _, err := p.ReadTag(context.Background(), tagged.service, two); !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Errorf("ReadTag(%s) of a revision no release tagged = %v, want a %s refusal", two, err, refusal.CodeNotReady)
	}
}

func TestUntaggingTakesTheTagOffItsRevisionAndLeavesTheTrafficWhereItWas(t *testing.T) {
	server := &runServer{}
	first := serves("ocel-shop-prod-app")
	first.tag = "r00000001"
	_, one := released(t, server, first)
	p := server.open(t)
	if _, err := p.Pin(context.Background(), first.service, one, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", one, err)
	}

	for range 2 {
		if err := p.Untag(context.Background(), first.service, "r00000001"); err != nil {
			t.Fatalf("Untag() = %v", err)
		}
	}

	traffic := server.serving().Traffic
	if slices.ContainsFunc(traffic, func(target *run.GoogleCloudRunV2TrafficTarget) bool { return target.Tag == "r00000001" }) {
		t.Errorf("the service carries %+v after the untag, want tag r00000001 gone", traffic)
	}
	if !servedBy(traffic, one) {
		t.Errorf("the service serves %+v after the untag, want all of it still on %s", traffic, one)
	}
}

func TestWhatAServicePinsIsTheRevisionTakingAllOfItsTraffic(t *testing.T) {
	server := &runServer{}
	first := serves("ocel-shop-prod-app")
	_, one := released(t, server, first)
	p := server.open(t)
	ctx := context.Background()

	if pinned, err := p.ReadServing(ctx, first.service); err != nil || pinned != one {
		t.Errorf("ReadServing() of a service following its latest revision = %q, %v, want %s, the revision it serves", pinned, err, one)
	}
	second := first
	second.image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	_, two := released(t, server, second)
	if _, err := p.Pin(ctx, first.service, two, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", two, err)
	}
	if pinned, err := p.ReadServing(ctx, first.service); err != nil || pinned != two {
		t.Errorf("ReadServing() = %q, %v, want %s", pinned, err, two)
	}
}

func TestAReleaseOntoAServiceThatServesItsLatestRevisionKeepsServingThatRevision(t *testing.T) {
	server := &runServer{}
	first := serves("ocel-shop-prod-app")
	_, one := released(t, server, first)

	second := first
	second.image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	released(t, server, second)

	if service := server.serving(); !servedBy(service.Traffic, one) {
		t.Errorf("the service serves %+v after the second release, want all of it on %s, the revision it served: a service that follows its latest revision hands traffic to whatever a release creates", service.Traffic, one)
	}
}

func TestAFunctionTheSpecGivesNoURLOfItsOwnIsLeftPrivate(t *testing.T) {
	server := &runServer{}

	released(t, server, serving{service: "ocel-shop-prod-fn", compute: provider.ComputeServerless})

	if desired := server.current(); desired.InvokerIamDisabled {
		t.Error("a function the plan says has no url may be invoked without an invoker check, " +
			"and a function reached only through its app is reachable by everybody instead")
	}
}

func TestAFunctionTheSpecGivesAURLIsReachedWithoutAnAllUsersGrant(t *testing.T) {
	server := &runServer{}

	released(t, server, serving{service: "ocel-shop-prod-fn", compute: provider.ComputeServerless, public: true})

	if desired := server.current(); !desired.InvokerIamDisabled {
		t.Error("a function the plan gives a url of its own still checks its invoker, and a service nobody may invoke answers nothing")
	}
	if bound := server.bound(); len(bound) != 0 {
		t.Errorf("the release wrote %d iam policies, want none: an org that restricts iam members to its own domain refuses an "+
			"allUsers binding outright, and the service is opened by turning the invoker check off instead", len(bound))
	}
}

func TestAContainerAppIsAlwaysOpenedToThePublic(t *testing.T) {
	server := &runServer{}

	released(t, server, serving{service: "ocel-shop-prod-app", compute: provider.ComputeContainer, health: "/", public: true})

	if desired := server.current(); !desired.InvokerIamDisabled {
		t.Error("the app's own front still checks its invoker, and nothing else sits between the internet and it")
	}
}

func TestAReleaseOntoADeployedServiceNeverHandsTrafficBackToTheLatestRevision(t *testing.T) {
	server := &runServer{}
	first := serving{
		service: "ocel-shop-prod-app",
		image:   "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:one",
		compute: provider.ComputeContainer,
		health:  "/",
		public:  true,
	}
	released(t, server, first)

	second := first
	second.image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	released(t, server, second)

	for _, patched := range server.releases() {
		if len(patched.Traffic) == 0 {
			t.Fatal("a patch named no traffic, and Cloud Run replaces the body it is handed: " +
				"the allocation this release pinned would go back to the latest revision, which is whatever ran last")
		}
		if patched.Traffic[0].Type != trafficByRevision || patched.Traffic[0].Revision == "" {
			t.Errorf("a patch allocated traffic by %q, want it by the name of a revision", patched.Traffic[0].Type)
		}
	}
}

func TestPinningTrafficLeavesWhoMayReachTheServiceAsTheReleaseSetIt(t *testing.T) {
	server := &runServer{}
	behind := serving{
		service: "ocel-shop-prod-web",
		compute: provider.ComputeServerless,
		public:  true,
		ingress: ingressLoadBalancer,
	}
	released(t, server, behind)
	behind.image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	released(t, server, behind)

	service := server.serving()
	if service.Ingress != ingressLoadBalancer {
		t.Errorf("the service takes %q after its traffic was pinned, want %q: a patch replaces what it is handed, so pinning traffic alone must say it changes the traffic alone",
			service.Ingress, ingressLoadBalancer)
	}
	if !service.InvokerIamDisabled {
		t.Error("the service checks its invoker again after its traffic was pinned, and every visitor to a public app would be refused")
	}
}

func serves(service string) serving {
	return serving{
		service: service,
		image:   "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:one",
		compute: provider.ComputeContainer,
		health:  "/",
		public:  true,
	}
}

func TestAServiceChangedUnderAReleaseIsReadAgainAndPatchedAgain(t *testing.T) {
	server := &runServer{}
	released(t, server, serves("ocel-shop-prod-app"))

	server.patchConflicts = 1
	again := serves("ocel-shop-prod-app")
	again.image = "europe-west1-docker.pkg.dev/acme/ocel/app:sha256-two"
	released(t, server, again)

	service := server.serving()
	if got := service.Template.Containers[0].Image; !strings.HasSuffix(got, "sha256-two") {
		t.Errorf("the service runs %q, want the image the second release named: a 409 says the read the write was built on is stale", got)
	}
}

func TestAServiceThatKeepsChangingUnderAReleaseIsRefusedRatherThanRetriedForever(t *testing.T) {
	server := &runServer{}
	released(t, server, serves("ocel-shop-prod-app"))

	server.patchConflicts = releaseAttempts + 1
	before := server.tries()
	_, err := server.open(t).deployService(context.Background(), serves("ocel-shop-prod-app"), nil)
	if err == nil {
		t.Fatal("deployService() over a service that never stops changing = nil, want the release refused")
	}
	if !strings.Contains(err.Error(), "ocel-shop-prod-app") {
		t.Errorf("deployService() = %v, want the service it was releasing named", err)
	}
	if patched := server.tries(); patched-before != releaseAttempts {
		t.Errorf("the release patched %d times, want %d: a retry that never gives up keeps a deploy open", patched-before, releaseAttempts)
	}
}

func TestARunSaysWhichCloudRunServiceItCreatesReleasesAndDeletesAndInWhichRegion(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	progress := &fake.Log{}
	ctx := context.Background()

	if _, err := p.deployService(ctx, serves("ocel-shop-prod-app"), progress); err != nil {
		t.Fatalf("deployService() = %v", err)
	}
	if _, err := p.deployService(ctx, serves("ocel-shop-prod-app"), progress); err != nil {
		t.Fatalf("deployService() again = %v", err)
	}
	if err := p.tearDown(ctx, "ocel-shop-prod-app", progress); err != nil {
		t.Fatalf("tearDown() = %v", err)
	}

	want := []string{
		"INFO Creating Cloud Run service ocel-shop-prod-app in europe-west1",
		"INFO Releasing a new revision of Cloud Run service ocel-shop-prod-app in europe-west1",
		"INFO Deleting Cloud Run service ocel-shop-prod-app in europe-west1",
	}
	if got := progress.Lines(); !slices.Equal(got, want) {
		t.Errorf("the run said %q, want %q", got, want)
	}
}

func TestAPolicyWriteRefusedAsConflictingPreconditionFailedOrAbortedIsStale(t *testing.T) {
	for name, err := range map[string]error{
		"http 409":          &googleapi.Error{Code: http.StatusConflict},
		"http 412":          &googleapi.Error{Code: http.StatusPreconditionFailed},
		"grpc aborted":      status.Error(codes.Aborted, "etag"),
		"grpc precondition": status.Error(codes.FailedPrecondition, "etag"),
	} {
		if !stale(err) {
			t.Errorf("stale(%s) = false, want true", name)
		}
	}
	for name, err := range map[string]error{
		"http 403":       &googleapi.Error{Code: http.StatusForbidden},
		"grpc not found": status.Error(codes.NotFound, "gone"),
		"nil":            nil,
	} {
		if stale(err) {
			t.Errorf("stale(%s) = true, want false", name)
		}
	}
}

func promotable(service string) serving {
	s := serves(service)
	s.opensOnPromotion = true
	return s
}

func TestABrandNewAppServiceAnswersNobodyUntilItsFirstPromotion(t *testing.T) {
	server := &runServer{}
	app := promotable("ocel-shop-prod-app")
	_, one := released(t, server, app)

	if server.serving().InvokerIamDisabled {
		t.Fatal("a service its first release created answers anyone before a promotion pinned it, and the ledger names nothing it serves")
	}
	if _, err := server.open(t).Pin(context.Background(), app.service, one, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", one, err)
	}
	if !server.serving().InvokerIamDisabled {
		t.Error("the service still checks its invoker once its first promotion pinned it, and every visitor to a public app would be refused")
	}

	second := app
	second.image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	released(t, server, second)
	if !server.serving().InvokerIamDisabled {
		t.Error("a later release closed the service its promotion opened, and every visitor would be refused until the next promotion")
	}
}

func TestClosingAServiceAnotherPromotionRepinnedSinceLeavesItOpen(t *testing.T) {
	server := &runServer{}
	app := promotable("ocel-shop-prod-app")
	_, one := released(t, server, app)
	p := server.open(t)
	if _, err := p.Pin(context.Background(), app.service, one, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", one, err)
	}
	writes := len(server.patched)
	repinned := errors.New("another promotion pinned this service since")

	err := p.Close(context.Background(), app.service, func(context.Context) error { return repinned })

	if !errors.Is(err, repinned) {
		t.Fatalf("Close() = %v, want the error that says the promotion is no longer the one pinned", err)
	}
	if !server.serving().InvokerIamDisabled {
		t.Error("the service was closed although another promotion pinned it since the check")
	}
	if got := len(server.patched); got != writes {
		t.Errorf("%d patches landed after the Close, want none", got-writes)
	}
}

func TestClosingAPromotedServiceTurnsItsInvokerCheckBackOn(t *testing.T) {
	server := &runServer{}
	app := promotable("ocel-shop-prod-app")
	_, one := released(t, server, app)
	p := server.open(t)
	if _, err := p.Pin(context.Background(), app.service, one, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", one, err)
	}

	for range 2 {
		if err := p.Close(context.Background(), app.service, nil); err != nil {
			t.Fatalf("Close() = %v", err)
		}
	}

	if server.serving().InvokerIamDisabled {
		t.Error("a closed service answers anyone, and the pointer that promoted it was taken back")
	}
	if !servedBy(server.serving().Traffic, one) {
		t.Errorf("closing the service moved its traffic to %+v", server.serving().Traffic)
	}
}

func TestClosingAnOpenServiceNoPromotionOpenedTurnsItsInvokerCheckBackOn(t *testing.T) {
	server := &runServer{}
	app := serves("ocel-shop-prod-app")
	app.public = true
	_, one := released(t, server, app)
	p := server.open(t)
	if _, err := p.Pin(context.Background(), app.service, one, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", one, err)
	}

	if err := p.Close(context.Background(), app.service, nil); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	if server.serving().InvokerIamDisabled {
		t.Error("a closed service answers anyone because it carries no label saying a promotion opened it, and the router says a removed pointer is served no more")
	}
}

func TestPinReportsWhetherItOpenedTheService(t *testing.T) {
	server := &runServer{}
	app := promotable("ocel-shop-prod-app")
	_, one := released(t, server, app)
	p := server.open(t)

	opened, err := p.Pin(context.Background(), app.service, one, nil)
	if err != nil || !opened {
		t.Errorf("the first Pin(%s) = %v, %v, want it to say it opened the service: a promotion that fails later has to close it again", one, opened, err)
	}
	opened, err = p.Pin(context.Background(), app.service, one, nil)
	if err != nil || opened {
		t.Errorf("Pin(%s) of an open service = %v, %v, want it to say it opened nothing", one, opened, err)
	}
}

func TestAPinWhoseOperationFailsStillReportsItOpenedTheService(t *testing.T) {
	server := &runServer{failRouting: true}
	app := promotable("ocel-shop-prod-app")
	_, one := released(t, server, app)
	p := server.open(t)

	opened, err := p.Pin(context.Background(), app.service, one, nil)

	if err == nil {
		t.Fatal("Pin = nil, want the failed operation")
	}
	if !opened {
		t.Error("Pin reports it opened nothing, and its patch opening the service was sent before the operation failed: a promotion putting services back would leave it open")
	}
}

func TestPinningBackWithoutOpeningLeavesAClosedServiceClosed(t *testing.T) {
	server := &runServer{}
	app := promotable("ocel-shop-prod-app")
	_, one := released(t, server, app)
	second := app
	second.image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	released(t, server, second)
	p := server.open(t)

	if err := p.Restore(context.Background(), app.service, one, nil); err != nil {
		t.Fatalf("Restore(%s) = %v", one, err)
	}

	if server.serving().InvokerIamDisabled {
		t.Error("pinning a closed service back opened it to anyone, and a promotion putting a service back closes it first so its old revision answers nobody")
	}
	if !servedBy(server.serving().Traffic, one) {
		t.Errorf("the service serves %+v, want all of it on %s", server.serving().Traffic, one)
	}
}

func TestARollbackIsNotRecordedOnTheRevisionTheServiceServes(t *testing.T) {
	ctx := context.Background()
	server := &runServer{}
	app := promotable("ocel-shop-prod-app")
	_, one := released(t, server, app)
	p := server.open(t)
	if _, err := p.Pin(ctx, app.service, one, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", one, err)
	}

	err := p.RecordRollback(ctx, app.service, one, pin.Rollback{Previous: "x"})

	if !errors.Is(err, pin.ErrRevisionServed) {
		t.Errorf("RecordRollback(%s) = %v, want %v: the service serves it", one, err, pin.ErrRevisionServed)
	}
	if _, found, err := p.ReadRollback(ctx, app.service, one); err != nil || found {
		t.Errorf("ReadRollback(%s) = found %v, %v, want nothing recorded for the revision the service serves", one, found, err)
	}
}

func TestARecordedRollbackIsReadBackUntilARevisionIsPinnedAgain(t *testing.T) {
	ctx := context.Background()
	server := &runServer{}
	app := promotable("ocel-shop-prod-app")
	_, one := released(t, server, app)
	second := app
	second.image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	_, two := released(t, server, second)
	p := server.open(t)

	recorded := pin.Rollback{Previous: one, Opened: true}
	if err := p.RecordRollback(ctx, app.service, two, recorded); err != nil {
		t.Fatalf("RecordRollback(%s) = %v", two, err)
	}
	third := app
	third.image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:three"
	released(t, server, third)
	if got, found, err := p.ReadRollback(ctx, app.service, two); err != nil || !found || got != recorded {
		t.Fatalf("ReadRollback(%s) after another release = %+v, %v, %v, want %+v: a release keeps what rollbacks the service records", two, got, found, err, recorded)
	}
	if _, found, err := p.ReadRollback(ctx, app.service, one); err != nil || found {
		t.Errorf("ReadRollback(%s) = found %v, %v, want nothing recorded for a revision no promotion rolled back", one, found, err)
	}

	if _, err := p.Pin(ctx, app.service, two, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", two, err)
	}
	if _, found, err := p.ReadRollback(ctx, app.service, two); err != nil || found {
		t.Errorf("ReadRollback(%s) after a promotion pinned it again = found %v, %v, want nothing: a promotion that lands on it makes it what the service serves",
			two, found, err)
	}
}

func TestARollbackRecordThatCannotBeDecodedIsReportedRatherThanReadAsNothing(t *testing.T) {
	ctx := context.Background()
	server := &runServer{}
	app := promotable("ocel-shop-prod-app")
	_, one := released(t, server, app)
	second := app
	second.image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	_, two := released(t, server, second)
	p := server.open(t)
	server.mu.Lock()
	server.service.Annotations = map[string]string{rollbacksAnnotation: "{not json"}
	server.mu.Unlock()

	if _, found, err := p.ReadRollback(ctx, app.service, one); err == nil || found {
		t.Errorf("ReadRollback(%s) = found %v, %v, want an error: the record cannot be decoded", one, found, err)
	}

	if _, err := p.Pin(ctx, app.service, two, nil); err != nil {
		t.Fatalf("Pin(%s) = %v, want a pin to replace a record it cannot decode", two, err)
	}
	server.mu.Lock()
	recorded := server.service.Annotations[rollbacksAnnotation]
	server.mu.Unlock()
	if recorded != "" {
		t.Errorf("the service records %q after the pin, want the undecodable record gone", recorded)
	}
}

func TestPinningAServiceNoPromotionOpensLeavesItsInvokerCheckOn(t *testing.T) {
	server := &runServer{}
	worker := serving{service: "ocel-shop-prod-worker", image: "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:one", compute: provider.ComputeServerless}
	_, one := released(t, server, worker)

	p := server.open(t)
	if _, err := p.Pin(context.Background(), worker.service, one, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", one, err)
	}
	if err := p.Close(context.Background(), worker.service, nil); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	if server.serving().InvokerIamDisabled {
		t.Error("pinning a service that only its invokers may reach opened it to anyone")
	}
}

func TestWarmingAPromotedRevisionAsksItsOwnUrlOnTheAppsHealthPath(t *testing.T) {
	asked := make(chan string, 4)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked <- r.Method + " " + r.URL.Path
	}))
	t.Cleanup(app.Close)
	server := &runServer{}
	open := serves("ocel-shop-prod-app")
	_, one := released(t, server, open)
	server.service.TrafficStatuses = []*run.GoogleCloudRunV2TrafficTargetStatus{{Type: trafficByRevision, Revision: one, Tag: "r00000001", Uri: app.URL}}

	if err := server.open(t).Warm(context.Background(), open.service, one, "/healthz"); err != nil {
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
	for name, tc := range map[string]struct {
		status  int
		wantErr bool
	}{
		"the proxy refusing the token":           {http.StatusForbidden, true},
		"the proxy finding no token":             {http.StatusUnauthorized, true},
		"an app failing to start":                {http.StatusServiceUnavailable, true},
		"an app with nothing at its health path": {http.StatusNotFound, false},
	} {
		t.Run(name, func(t *testing.T) {
			app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status) }))
			t.Cleanup(app.Close)
			server := &runServer{}
			open := serves("ocel-shop-prod-app")
			_, one := released(t, server, open)
			server.service.TrafficStatuses = []*run.GoogleCloudRunV2TrafficTargetStatus{{Type: trafficByRevision, Revision: one, Tag: "r00000001", Uri: app.URL}}

			err := server.open(t).Warm(context.Background(), open.service, one, "/healthz")

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
	server := &runServer{}
	behind := serves("ocel-shop-prod-app")
	behind.ingress = ingressLoadBalancer
	_, one := released(t, server, behind)
	server.service.TrafficStatuses = []*run.GoogleCloudRunV2TrafficTargetStatus{{Type: trafficByRevision, Revision: one, Tag: "r00000001", Uri: app.URL}}

	if err := server.open(t).Warm(context.Background(), behind.service, one, "/"); err != nil {
		t.Fatalf("Warm() = %v", err)
	}
	if len(asked) != 0 {
		t.Error("warming asked a revision whose ingress admits the load balancer alone, and the request could only be turned away")
	}
}

func TestWarmingARevisionBehindIdentityAwareProxySendsATokenSignedAsTheAccountTheServiceRunsAs(t *testing.T) {
	asked := make(chan string, 4)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked <- r.Method + " " + r.URL.Path + " " + r.Header.Get("Authorization")
	}))
	t.Cleanup(app.Close)
	server := &runServer{}
	preview := serves("ocel-shop-pr-7-web")
	preview.public = false
	preview.iap = true
	_, one := released(t, server, preview)
	server.service.TrafficStatuses = []*run.GoogleCloudRunV2TrafficTargetStatus{{Type: trafficByRevision, Revision: one, Tag: "r00000001", Uri: app.URL}}

	if err := server.open(t).Warm(context.Background(), preview.service, one, "/healthz"); err != nil {
		t.Fatalf("Warm() = %v", err)
	}

	select {
	case got := <-asked:
		if got != "HEAD /healthz Bearer signed-by-iam" {
			t.Errorf("warming asked %q, want HEAD /healthz carrying the token IAM signed: the proxy turns away a request that carries none", got)
		}
	default:
		t.Fatal("warming asked a revision behind Identity-Aware Proxy nothing, and the first viewer after the promotion pays its cold start")
	}
	account := preview.account
	signed := server.signedJWTs()
	if len(signed) != 1 || signed[0].account != "projects/-/serviceAccounts/"+account {
		t.Fatalf("IAM was asked to sign %+v, want one token signed as %s", signed, account)
	}
	claims := signed[0].claims
	if claims["iss"] != account || claims["sub"] != account || claims["aud"] != app.URL+"/*" {
		t.Errorf("the token claims %v, want iss and sub %s and aud %s/*: the proxy admits a service account's JWT only for the url it names", claims, account, app.URL)
	}
	if issued, expires := claims["iat"].(float64), claims["exp"].(float64); expires <= issued || expires-issued > 3600 {
		t.Errorf("the token is issued at %v and expires at %v, and the proxy takes one that lives for at most an hour", issued, expires)
	}
}

func TestWarmingThroughALoadBalancerDialsItsAddressAndAsksForTheHostname(t *testing.T) {
	asked := make(chan string, 4)
	balancer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked <- r.Method + " " + r.Host + r.URL.Path
	}))
	t.Cleanup(balancer.Close)
	roots := x509.NewCertPool()
	roots.AddCert(balancer.Certificate())
	warmRoots = roots
	t.Cleanup(func() { warmRoots = nil })

	if err := warmThrough(context.Background(), "https://example.com/healthz", balancer.Listener.Addr().String()); err != nil {
		t.Fatalf("warmThrough() = %v", err)
	}

	select {
	case got := <-asked:
		if got != "HEAD example.com/healthz" {
			t.Errorf("the load balancer was asked %q, want HEAD example.com/healthz: its url map routes by hostname", got)
		}
	default:
		t.Error("warming asked the load balancer nothing")
	}
}

func TestAReleaseBehindIdentityAwareProxyRecordsTheUrlOfTheTagItGaveItsRevision(t *testing.T) {
	server := &runServer{}
	preview := serves("ocel-shop-pr-7-web")
	preview.public = false
	preview.iap = true
	preview.tag = "r00000001"

	deployed, err := server.open(t).deployService(context.Background(), preview, nil)
	if err != nil {
		t.Fatalf("deployService() = %v", err)
	}
	if want := "https://r00000001---ocel-shop-pr-7-web.run.app"; deployed.deployment != want {
		t.Errorf("the release recorded deployment url %q, want %q: Cloud Run answers the tagged revision there, behind the service's proxy", deployed.deployment, want)
	}
}

func TestAReleaseBehindTheLoadBalancerRecordsNoDeploymentUrl(t *testing.T) {
	server := &runServer{}
	behind := serves("ocel-shop-pr-7-web")
	behind.ingress = ingressLoadBalancer
	behind.tag = "r00000001"

	deployed, err := server.open(t).deployService(context.Background(), behind, nil)
	if err != nil {
		t.Fatalf("deployService() = %v", err)
	}
	if deployed.deployment != "" {
		t.Errorf("the release recorded deployment url %q, and its ingress turns away everything but the load balancer, which answers the deployment on a hostname of its own", deployed.deployment)
	}
}

func TestAReleaseRetriedAfterAConflictKeepsOnlyTheRollbacksTheServiceRecordsOnTheRetry(t *testing.T) {
	ctx := context.Background()
	server := &runServer{}
	app := promotable("ocel-shop-prod-app")
	_, one := released(t, server, app)
	p := server.open(t)
	if _, err := p.Pin(ctx, app.service, one, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", one, err)
	}
	if err := p.RecordRollback(ctx, app.service, "rev-x", pin.Rollback{Previous: one}); err != nil {
		t.Fatalf("RecordRollback(rev-x) = %v", err)
	}
	server.patchConflicts = 1
	server.onConflict = func(s *run.GoogleCloudRunV2Service) { s.Annotations = nil }

	second := app
	second.image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	released(t, server, second)

	if got := server.serving().Annotations[rollbacksAnnotation]; got != "" {
		t.Errorf("the service records rollbacks %q after the retried release, want none: the retry read a service that recorded none", got)
	}
}
