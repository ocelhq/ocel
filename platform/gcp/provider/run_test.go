package gcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	run "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestAServerlessRevisionScalesToNothingAndIsBilledPerRequest(t *testing.T) {
	desired := serviceOf(serving{
		service: "ocel-shop-prod-web",
		image:   "europe-west1-docker.pkg.dev/acme/ocel/web@sha256:abc",
		account: "ocel-production@acme.iam.gserviceaccount.com",
		compute: providerkit.ComputeServerless,
	})

	template := desired.Template
	if template.Scaling.MinInstanceCount != 0 {
		t.Errorf("a function keeps %d instances warm, want none: it is billed per request", template.Scaling.MinInstanceCount)
	}
	container := template.Containers[0]
	if !container.Resources.CpuIdle {
		t.Error("a function holds its cpu between requests, want it idle: that is what request-based billing is")
	}
	if container.StartupProbe != nil {
		t.Error("a function carries a startup probe, and nothing declared a path to probe")
	}
	if got := container.Resources.Limits; got["cpu"] != "1" || got["memory"] != "512Mi" {
		t.Errorf("a revision asks for %v, want one vCPU and 512Mi", got)
	}
	if len(container.Ports) != 1 || container.Ports[0].ContainerPort != providerkit.InjectedPort {
		t.Errorf("a revision listens on %v, want %d, which is the port every image ocel builds binds",
			container.Ports, providerkit.InjectedPort)
	}
	if template.ServiceAccount != "ocel-production@acme.iam.gserviceaccount.com" {
		t.Errorf("a revision runs as %q, want the class's own runtime account", template.ServiceAccount)
	}
}

func TestAContainerRevisionKeepsAnInstanceUpAndIsProbedOnItsOwnPath(t *testing.T) {
	desired := serviceOf(serving{
		service: "ocel-shop-prod-api",
		image:   "europe-west1-docker.pkg.dev/acme/ocel/api@sha256:abc",
		compute: providerkit.ComputeContainer,
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
		t.Fatal("a container carries no startup probe, and up means a 2xx on the path the wire named")
	}
	if got := container.StartupProbe.HttpGet; got.Path != "/healthz" || got.Port != providerkit.InjectedPort {
		t.Errorf("a container is probed at %v, want /healthz on %d", got, providerkit.InjectedPort)
	}
}

func TestARevisionCarriesEveryValueTheDeployResolvedInOneOrder(t *testing.T) {
	desired := serviceOf(serving{
		service: "ocel-shop-prod-web",
		image:   "web@sha256:abc",
		compute: providerkit.ComputeServerless,
		env:     map[string]string{"DATABASE_URL": "postgres://", "API_KEY": "k"},
	})

	var carried []string
	for _, entry := range desired.Template.Containers[0].Env {
		carried = append(carried, entry.Name+"="+entry.Value)
	}
	if got := strings.Join(carried, " "); got != "API_KEY=k DATABASE_URL=postgres://" {
		t.Errorf("a revision carries %q, want every value in one order, so an unchanged deploy asks for no new revision", got)
	}
}

func TestTrafficIsPinnedToOneRevisionByName(t *testing.T) {
	pinned := trafficTo("ocel-shop-prod-web-00007-abc")

	if len(pinned) != 1 {
		t.Fatalf("traffic is split %d ways, want all of it on the revision this release stood up", len(pinned))
	}
	if pinned[0].Type != trafficByRevision {
		t.Errorf("traffic is allocated by %q, want %q: the latest revision is whatever ran last, which is not what this release promised",
			pinned[0].Type, trafficByRevision)
	}
	if pinned[0].Revision != "ocel-shop-prod-web-00007-abc" || pinned[0].Percent != 100 {
		t.Errorf("traffic = %+v, want all of it on the named revision", pinned[0])
	}
}

func TestAnEmulatedRunFindsTheImageUnderTheNameTheDaemonHoldsItBy(t *testing.T) {
	pinned := "europe-west1-docker.pkg.dev/floci/ocel-preview/web@sha256:abc123"

	real := pushing(t, "").runnable(pinned)
	if real != pinned {
		t.Errorf("runnable() = %q against a registry, want the digest the release pinned", real)
	}

	emulated := pushing(t, "http://127.0.0.1:4588").runnable(pinned)
	if emulated != "europe-west1-docker.pkg.dev/floci/ocel-preview/web:sha256-abc123" {
		t.Errorf("runnable() = %q under emulation, want the tag the image was written into the daemon under: "+
			"a docker daemon resolves nothing by the digest a registry would have given it", emulated)
	}
}

func TestThePublicIsBoundToInvokeAServiceOnceAndNotTwice(t *testing.T) {
	held := []*policyBinding{{Role: invokerRole, Members: []string{"user:someone@acme.com"}}}

	bound, changed := invokable(held)
	if !changed {
		t.Fatal("a policy with nobody public bound was left alone, and a service nobody may invoke answers nothing")
	}
	if _, again := invokable(bound); again {
		t.Error("binding the public a second time changed the policy again, so every deploy would write one")
	}
}

func released(t *testing.T, server *runServer, s serving) string {
	t.Helper()
	if s.image == "" {
		s.image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:abc"
	}
	uri, err := server.open(t).stand(context.Background(), s, nil)
	if err != nil {
		t.Fatalf("stand(%s) = %v", s.service, err)
	}
	return uri
}

func publicly(policies []*run.GoogleIamV1Policy) bool {
	for _, policy := range policies {
		for _, binding := range policy.Bindings {
			if binding.Role == invokerRole && slices.Contains(binding.Members, everyone) {
				return true
			}
		}
	}
	return false
}

func TestAFunctionThePlanGivesNoURLOfItsOwnIsLeftPrivate(t *testing.T) {
	server := &runServer{}

	released(t, server, serving{service: "ocel-shop-prod-fn", compute: providerkit.ComputeServerless})

	if publicly(server.bound()) {
		t.Error("a function the plan says has no url was bound for anyone on the internet to invoke, " +
			"and a function reached only through its app is reachable by everybody instead")
	}
}

func TestAFunctionThePlanGivesAURLIsOpenedToThePublic(t *testing.T) {
	server := &runServer{}

	released(t, server, serving{service: "ocel-shop-prod-fn", compute: providerkit.ComputeServerless, public: true})

	if !publicly(server.bound()) {
		t.Error("a function the plan gives a url of its own was left private, and a service nobody may invoke answers nothing")
	}
}

func TestAContainerAppIsAlwaysOpenedToThePublic(t *testing.T) {
	server := &runServer{}

	released(t, server, serving{service: "ocel-shop-prod-app", compute: providerkit.ComputeContainer, health: "/", public: true})

	if !publicly(server.bound()) {
		t.Error("the app's own front was left private, and nothing else stands between the internet and it")
	}
}

func TestAReleaseOntoAStandingServiceNeverHandsTrafficBackToTheLatestRevision(t *testing.T) {
	server := &runServer{}
	first := serving{
		service: "ocel-shop-prod-app",
		image:   "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:one",
		compute: providerkit.ComputeContainer,
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
				"the allocation this release held would go back to the latest revision, which is whatever ran last")
		}
		if patched.Traffic[0].Type != trafficByRevision || patched.Traffic[0].Revision == "" {
			t.Errorf("a patch allocated traffic by %q, want it by the name of a revision", patched.Traffic[0].Type)
		}
	}
	standing := server.serving()
	if !servedBy(standing.Traffic, revisionName(standing.LatestReadyRevision)) {
		t.Errorf("the service serves %+v, want all of it on the revision the second release stood up", standing.Traffic)
	}
}

func serves(service string) serving {
	return serving{
		service: service,
		image:   "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:one",
		compute: providerkit.ComputeContainer,
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

	standing := server.serving()
	if !servedBy(standing.Traffic, revisionName(standing.LatestReadyRevision)) {
		t.Errorf("the service serves %+v after a patch a concurrent write refused, want the release it asked for", standing.Traffic)
	}
	if got := standing.Template.Containers[0].Image; !strings.HasSuffix(got, "sha256-two") {
		t.Errorf("the service runs %q, want the image the second release named: a 409 says the read the write was built on is stale", got)
	}
}

func TestAServiceThatKeepsChangingUnderAReleaseIsRefusedRatherThanRetriedForever(t *testing.T) {
	server := &runServer{}
	released(t, server, serves("ocel-shop-prod-app"))

	server.patchConflicts = releaseAttempts + 1
	before, _ := server.tries()
	_, err := server.open(t).stand(context.Background(), serves("ocel-shop-prod-app"), nil)
	if err == nil {
		t.Fatal("stand() over a service that never settles = nil, want the release refused")
	}
	if !strings.Contains(err.Error(), "ocel-shop-prod-app") {
		t.Errorf("stand() = %v, want the service it was releasing named", err)
	}
	if patched, _ := server.tries(); patched-before != releaseAttempts {
		t.Errorf("the release patched %d times, want %d: a retry that never gives up holds a deploy open", patched-before, releaseAttempts)
	}
}

func TestAPolicyChangedUnderAReleaseIsReadAgainAndWrittenAgain(t *testing.T) {
	server := &runServer{policyConflicts: 1}

	released(t, server, serves("ocel-shop-prod-app"))

	if !publicly(server.bound()) {
		t.Error("the binding a concurrent policy write refused was never made again, and a service nobody may invoke answers nothing")
	}
	if _, policies := server.tries(); policies != 2 {
		t.Errorf("the release wrote the policy %d times, want 2: a 409 says the read the write was built on is stale", policies)
	}
}

func TestAPolicyThatKeepsChangingUnderAReleaseIsRefusedRatherThanRetriedForever(t *testing.T) {
	server := &runServer{policyConflicts: releaseAttempts + 1}

	_, err := server.open(t).stand(context.Background(), serves("ocel-shop-prod-app"), nil)
	if err == nil {
		t.Fatal("stand() over a policy that never settles = nil, want the release refused rather than left private")
	}
	if _, policies := server.tries(); policies != releaseAttempts {
		t.Errorf("the release wrote the policy %d times, want %d: a retry that never gives up holds a deploy open", policies, releaseAttempts)
	}
}
