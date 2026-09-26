package gcp

import (
	"context"
	"strings"
	"testing"
	"time"

	run "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/providerkit/appbuild"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
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
		account: "ocel-production@acme.iam.gserviceaccount.com",
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
	if len(container.Ports) != 1 || container.Ports[0].ContainerPort != appbuild.InjectedPort {
		t.Errorf("a revision listens on %v, want %d, which is the port every image ocel builds binds",
			container.Ports, appbuild.InjectedPort)
	}
	if template.ServiceAccount != "ocel-production@acme.iam.gserviceaccount.com" {
		t.Errorf("a revision runs as %q, want the class's own runtime account", template.ServiceAccount)
	}
}

func TestAContainerRevisionKeepsAnInstanceUpAndIsProbedOnItsOwnPath(t *testing.T) {
	desired := desiredOf(t, serving{
		service: "ocel-shop-prod-api",
		image:   "europe-west1-docker.pkg.dev/acme/ocel/api@sha256:abc",
		compute: provider.ComputeContainer,
		health:  "/healthz",
	})

	template := desired.Template
	if template.Scaling.MinInstanceCount != 1 {
		t.Errorf("a container keeps %d instances up, want one: it is billed for the instance rather than the request", template.Scaling.MinInstanceCount)
	}
	container := template.Containers[0]
	if container.Resources.CpuIdle {
		t.Error("a container's cpu idles between requests, and a container is paid for by the instance: it keeps its cpu")
	}
	if container.StartupProbe == nil || container.StartupProbe.HttpGet == nil {
		t.Fatal("a container has no startup probe, and up means a 2xx on the path the wire named")
	}
	if got := container.StartupProbe.HttpGet; got.Path != "/healthz" || got.Port != appbuild.InjectedPort {
		t.Errorf("a container is probed at %v, want /healthz on %d", got, appbuild.InjectedPort)
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

func TestAFunctionThatNamesNoMemoryOrTimeoutKeepsTheProfileTheClassRunsOn(t *testing.T) {
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
	pinned := trafficTo("ocel-shop-prod-web-00007-abc")

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

func TestAnEmulatedRunFindsTheImageUnderTheNameTheDaemonStoresItUnder(t *testing.T) {
	pinned := "europe-west1-docker.pkg.dev/floci/ocel-preview/web@sha256:abc123"

	real := pushing(t, "").storedAs(pinned)
	if real != pinned {
		t.Errorf("storedAs() = %q against a registry, want the digest the release pinned", real)
	}

	emulated := pushing(t, "http://127.0.0.1:4588").storedAs(pinned)
	if emulated != "europe-west1-docker.pkg.dev/floci/ocel-preview/web:sha256-abc123" {
		t.Errorf("storedAs() = %q under emulation, want the tag the image was written into the daemon under: "+
			"a docker daemon resolves nothing by the digest a registry would have given it", emulated)
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

func TestAReleaseReportsTheRevisionItPinnedSoALaterPromoteCanReachIt(t *testing.T) {
	server := &runServer{}
	first := serves("ocel-shop-prod-app")
	_, one := released(t, server, first)

	second := first
	second.image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	_, two := released(t, server, second)

	if one == "" || two == "" {
		t.Fatalf("a release created revisions %q and %q, want each named: a rollback pins the revision its promotion recorded", one, two)
	}
	if one == two {
		t.Errorf("both releases created revision %q, want a revision apiece: a rollback to the first would pin what the second serves", one)
	}
	service := server.serving()
	if !servedBy(service.Traffic, two) {
		t.Errorf("the service serves %+v, want all of it on %s, the revision the second release reported", service.Traffic, two)
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
	service := server.serving()
	if !servedBy(service.Traffic, revisionName(service.LatestReadyRevision)) {
		t.Errorf("the service serves %+v, want all of it on the revision the second release created", service.Traffic)
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
	again.image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	released(t, server, again)

	service := server.serving()
	if !servedBy(service.Traffic, revisionName(service.LatestReadyRevision)) {
		t.Errorf("the service serves %+v after a patch a concurrent write refused, want the release it asked for", service.Traffic)
	}
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
