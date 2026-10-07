package gcp

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/api/iam/v1"
	run "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/platform/gcp/provider/relay"
)

func bastionNames() Names { return Names{namespace: provider.Namespace("ocel"), project: "acme-prod"} }

func desiredBastion(t *testing.T, tier environment.Tier) *run.GoogleCloudRunV2Service {
	t.Helper()
	b := bastion{clients: &clients{Names: bastionNames(), region: "europe-west1"}}
	desired, err := serviceOf(b.serving(tier, "europe-west1-docker.pkg.dev/acme-prod/ocel-acme-prod-production/ocel-bastion:abc"))
	if err != nil {
		t.Fatalf("serviceOf() = %v", err)
	}
	return desired
}

func TestTheBastionReachesOnlyPrivateRangesOverTheNetworkOfItsTier(t *testing.T) {
	t.Parallel()

	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		access := desiredBastion(t, tier).Template.VpcAccess
		if access == nil || len(access.NetworkInterfaces) != 1 {
			t.Fatalf("the %s bastion has VPC access %+v, want one network interface", tier, access)
		}
		names := bastionNames()
		if got := access.NetworkInterfaces[0]; got.Network != names.NetworkPath(tier) || got.Subnetwork != names.SubnetworkPath("europe-west1", tier) {
			t.Errorf("the %s bastion joins %s on %s, want the tier's own network and subnetwork", tier, got.Network, got.Subnetwork)
		}
		if access.Egress != "PRIVATE_RANGES_ONLY" {
			t.Errorf("the %s bastion's egress is %q, want private ranges alone", tier, access.Egress)
		}
	}
}

func TestTheBastionIsReachableFromAnywhereYetOnlyByAnIdentityHoldingTheInvokerRole(t *testing.T) {
	t.Parallel()

	desired := desiredBastion(t, environment.TierProduction)
	if desired.Ingress != ingressEverywhere {
		t.Errorf("Ingress = %q, want %q: the caller is outside the network", desired.Ingress, ingressEverywhere)
	}
	if desired.InvokerIamDisabled || desired.IapEnabled {
		t.Errorf("the bastion skips Cloud Run's invoker check (invoker disabled %v, IAP %v), and anyone could then reach a private address", desired.InvokerIamDisabled, desired.IapEnabled)
	}
	if want := []string{bastionNames().Bastion(environment.TierProduction)}; !slices.Equal(desired.CustomAudiences, want) {
		t.Errorf("CustomAudiences = %v, want %v, the audience an identity token is minted for before the service has a URL", desired.CustomAudiences, want)
	}
}

func TestAnIdleBastionCostsNothingAndABusyOneIsBounded(t *testing.T) {
	t.Parallel()

	template := desiredBastion(t, environment.TierProduction).Template
	if template.Scaling.MinInstanceCount != 0 {
		t.Errorf("MinInstanceCount = %d, want 0: an idle bastion runs nothing", template.Scaling.MinInstanceCount)
	}
	if template.Scaling.MaxInstanceCount == 0 {
		t.Error("MaxInstanceCount = 0, want a ceiling on what a runaway build can run")
	}
	if !template.Containers[0].Resources.CpuIdle {
		t.Error("CpuIdle = false, and the bastion is billed per request, which an open socket keeps active")
	}
	if template.Timeout != "3600s" {
		t.Errorf("Timeout = %q, want 3600s, the longest Cloud Run holds a socket: a build holds its forwards that long", template.Timeout)
	}
}

func TestTheBastionRunsAsItsOwnAccountAndForwardsToPostgresAndValkeyOnItsTiersSubnetworkAlone(t *testing.T) {
	t.Parallel()

	desired := desiredBastion(t, environment.TierProduction)
	names := bastionNames()
	if desired.Template.ServiceAccount != names.BastionAccountEmail(environment.TierProduction) {
		t.Errorf("the bastion runs as %q, want its own account, which holds no role", desired.Template.ServiceAccount)
	}
	if got := envOf(desired.Template.Containers[0])[relay.AllowedEnv]; got != "10.240.0.0/20:5432,10.240.0.0/20:6379" {
		t.Errorf("%s = %q, want the Postgres and Valkey ports on the subnetwork the tier's Private Service Connect endpoints take their addresses from", relay.AllowedEnv, got)
	}
}

func TestEachTierHasItsOwnBastionAndAccount(t *testing.T) {
	t.Parallel()

	names := bastionNames()
	if names.Bastion(environment.TierProduction) == names.Bastion(environment.TierPreview) ||
		names.BastionAccount(environment.TierProduction) == names.BastionAccount(environment.TierPreview) {
		t.Error("the production and preview tiers share a bastion name or account")
	}
	if !cloudRunService.MatchString(names.Bastion(environment.TierPreview)) {
		t.Errorf("Bastion() = %q, which Cloud Run will not take as a service name", names.Bastion(environment.TierPreview))
	}
	if id := names.BastionAccount(environment.TierPreview); len(id) > maxAccountID {
		t.Errorf("BastionAccount() = %q is %d characters, and IAM takes %d", id, len(id), maxAccountID)
	}
}

type bastionHarness struct {
	run     *runServer
	b       bastion
	relay   *httptest.Server
	allowed []string

	mu       sync.Mutex
	pushed   []string
	proved   []string
	proveErr error
}

func newBastionHarness(t *testing.T, relayAllows ...string) *bastionHarness {
	t.Helper()
	h := &bastionHarness{run: &runServer{}, allowed: relayAllows}
	h.run.identities().accounts = map[string]bool{}
	h.run.identities().accountPolicies = map[string]*iam.Policy{}
	p := h.run.open(t)
	h.relay = httptest.NewServer(relay.NewHandler(destinationsOf(t, relayAllows...)))
	t.Cleanup(h.relay.Close)
	h.b = p.openBastion(p.resolved)
	h.b.pushBinary = func(_ context.Context, _ environment.Tier, _, ref string, binary []byte, _ string) error {
		if len(binary) == 0 {
			t.Error("the bastion image was pushed with no binary in it")
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		h.pushed = append(h.pushed, ref)
		return nil
	}
	h.b.prove = func(_ context.Context, audience string) (envsource.IdentityProof, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.proved = append(h.proved, audience)
		if h.proveErr != nil {
			return envsource.IdentityProof{}, h.proveErr
		}
		return envsource.IdentityProof{IDToken: "id-token"}, nil
	}
	h.b.open = func(ctx context.Context, link relay.Link) (*relay.Forward, error) {
		link.URL = h.relay.URL
		return relay.OpenForward(ctx, link)
	}
	h.b.waited = func(context.Context, int) bool { return true }
	return h
}

func (h *bastionHarness) forward(t *testing.T, targets ...string) []*relay.Forward {
	t.Helper()
	forwards, err := h.b.forwardPorts(context.Background(), environment.TierProduction, targets, nil)
	if err != nil {
		t.Fatalf("forwardPorts(%v) = %v", targets, err)
	}
	t.Cleanup(func() {
		for _, forward := range forwards {
			forward.Close()
		}
	})
	return forwards
}

func (h *bastionHarness) pushes() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.pushed)
}

func TestTheFirstForwardCreatesTheBastionItsAccountAndTheInvokerGrantAndLaterOnesReuseThem(t *testing.T) {
	t.Parallel()
	target := echoTarget(t)
	h := newBastionHarness(t, target)

	h.forward(t, target)
	before := len(h.run.releases())
	h.forward(t, target)

	service := h.run.serving()
	if service == nil || !strings.HasSuffix(service.Name, "/"+bastionNames().Bastion(environment.TierProduction)) {
		t.Fatalf("the bastion service is %+v, want the production tier's", service)
	}
	if got := len(h.run.created); got != 1 {
		t.Errorf("the bastion was created %d times, want once", got)
	}
	if got := len(h.run.releases()) - before; got != 0 {
		t.Errorf("the second forward released the bastion %d times, want it reused as it is", got)
	}
	if got := h.pushes(); len(got) != 1 {
		t.Errorf("the bastion image was pushed %d times, want once: the service already runs it", len(got))
	}
	created := h.run.identities().created
	if len(created) != 1 || created[0].AccountId != bastionNames().BastionAccount(environment.TierProduction) {
		t.Errorf("service accounts created = %+v, want the production bastion's alone", created)
	}
}

func TestTheFirstForwardSaysItMakesTheBastionAndLaterOnesSayNothing(t *testing.T) {
	t.Parallel()
	target := echoTarget(t)
	h := newBastionHarness(t, target)
	first, later := &fake.Log{}, &fake.Log{}

	for _, said := range []*fake.Log{first, later} {
		forwards, err := h.b.forwardPorts(context.Background(), environment.TierProduction, []string{target}, said)
		if err != nil {
			t.Fatalf("forwardPorts() = %v", err)
		}
		for _, forward := range forwards {
			forward.Close()
		}
	}

	names := bastionNames()
	want := []string{
		"INFO Making the bastion " + names.Bastion(environment.TierProduction) + " that forwards ports into the production tier's network",
		"INFO Pushing the bastion image " + h.b.imageRef(environment.TierProduction),
		"INFO Creating Cloud Run service " + names.Bastion(environment.TierProduction) + " in europe-west1",
	}
	if got := first.Lines(); !slices.Equal(got, want) {
		t.Errorf("the first forward said %q, want %q", got, want)
	}
	if got := later.Lines(); len(got) != 0 {
		t.Errorf("a forward through a bastion already made said %q, want nothing", got)
	}
}

func TestTheDeployIdentityAloneMayInvokeTheBastion(t *testing.T) {
	t.Parallel()
	target := echoTarget(t)
	h := newBastionHarness(t, target)

	h.forward(t, target)

	policies := h.run.bound()
	if len(policies) == 0 {
		t.Fatal("no policy was set on the bastion, so nothing may invoke it")
	}
	for _, policy := range policies {
		for _, binding := range policy.Bindings {
			if slices.Contains(binding.Members, "allUsers") || slices.Contains(binding.Members, "allAuthenticatedUsers") {
				t.Errorf("the bastion's policy binds %v to %s", binding.Members, binding.Role)
			}
		}
	}
	last := policies[len(policies)-1]
	if len(last.Bindings) != 1 || last.Bindings[0].Role != invokerRole || !slices.Equal(last.Bindings[0].Members, []string{"user:" + emulatorPrincipal}) {
		t.Errorf("the bastion's policy is %+v, want run.invoker for the deploy identity alone", last.Bindings)
	}
}

func TestAForwardToAnotherEnvironmentsTargetReusesTheBastionAsItIs(t *testing.T) {
	t.Parallel()
	first, second := echoTarget(t), echoTarget(t)
	h := newBastionHarness(t, first, second)
	h.forward(t, first)
	before := len(h.run.releases())

	h.forward(t, second)

	if got := len(h.run.releases()) - before; got != 0 {
		t.Errorf("a forward to another target released the bastion %d times, want none: forwards of one tier never rewrite what another relies on", got)
	}
	if got := h.pushes(); len(got) != 1 {
		t.Errorf("the image was pushed %d times, want once", len(got))
	}
}

func TestABastionRunningAnOlderRelayIsReleasedAgainAndServesTheNewRevision(t *testing.T) {
	t.Parallel()
	target := echoTarget(t)
	h := newBastionHarness(t, target)
	h.forward(t, target)
	h.run.mu.Lock()
	h.run.service.Template.Containers[0].Image += "-older"
	h.run.mu.Unlock()

	h.forward(t, target)

	service := h.run.serving()
	if served, latest := servedRevision(service), revisionName(service.LatestReadyRevision); served != latest {
		t.Errorf("the bastion serves %q, want %q, the revision its release made: an upgraded relay otherwise never carries a byte", served, latest)
	}
	if got := h.pushes(); len(got) != 2 {
		t.Errorf("the bastion image was pushed %d times, want twice: the service ran another relay", len(got))
	}
}

func TestABastionWhoseLatestRevisionServesNoTrafficIsRoutedToItOnTheNextForward(t *testing.T) {
	t.Parallel()
	target := echoTarget(t)
	h := newBastionHarness(t, target)
	h.forward(t, target)
	h.run.mu.Lock()
	h.run.service.Traffic = []*run.GoogleCloudRunV2TrafficTarget{{Type: trafficByRevision, Revision: "an-older-revision", Percent: 100}}
	h.run.mu.Unlock()

	h.forward(t, target)

	service := h.run.serving()
	if served, latest := servedRevision(service), revisionName(service.LatestReadyRevision); served != latest {
		t.Errorf("the bastion serves %q, want %q: a release that came ready and was never routed is routed on the next forward", served, latest)
	}
}

func TestForwardsAreOpenedOnLoopbackAndCarryBytesToTheirTargetsThroughTheBastion(t *testing.T) {
	t.Parallel()
	postgres, cache := echoTarget(t), echoTarget(t)
	h := newBastionHarness(t, postgres, cache)

	forwards := h.forward(t, postgres, cache)

	if len(forwards) != 2 {
		t.Fatalf("forwardPorts() = %+v, want one forward per target", forwards)
	}
	for i, forward := range forwards {
		if !strings.HasPrefix(forward.Address(), "127.0.0.1:") {
			t.Errorf("forward %d listens on %q, want a loopback port", i, forward.Address())
		}
		if got := echoed(t, forward.Address(), "ping"); got != "ping" {
			t.Errorf("forward %d answered %q, want its target's echo", i, got)
		}
	}
}

func TestAUsersOwnLoginIsRefusedBeforeAnythingIsCreated(t *testing.T) {
	t.Parallel()
	target := echoTarget(t)
	h := newBastionHarness(t, target)
	h.proveErr = errTestUserLogin

	_, err := h.b.forwardPorts(context.Background(), environment.TierProduction, []string{target}, nil)

	if err == nil || !strings.Contains(err.Error(), errTestUserLogin.Error()) {
		t.Fatalf("forwardPorts() = %v, want the message the identity proof gave", err)
	}
	if h.run.serving() != nil || len(h.pushes()) != 0 || len(h.run.identities().created) != 0 {
		t.Error("a bastion, an image or an account was created for a credential that cannot mint an identity token")
	}
}

func TestAForwardTheBastionCannotOpenStopsTheOnesAlreadyOpenedAndLeavesTheBuildWithoutTheBinding(t *testing.T) {
	t.Parallel()
	reachable, unreachable := echoTarget(t), echoTarget(t)
	h := newBastionHarness(t, reachable)

	forwards, err := h.b.forwardPorts(context.Background(), environment.TierProduction, []string{reachable, unreachable}, nil)

	if err == nil || len(forwards) != 0 {
		t.Fatalf("forwardPorts() = %v, %v, want a refusal and no forward", forwards, err)
	}
	if code, refused := provider.RefusedCode(err); !refused || code != "not-ready" {
		t.Errorf("forwardPorts() = %v, want a not-ready refusal, which the CLI answers by building without the binding", err)
	}
}

func TestTakingTheBastionDownDeletesItsServiceAndItsAccount(t *testing.T) {
	t.Parallel()
	target := echoTarget(t)
	h := newBastionHarness(t, target)
	h.forward(t, target)

	if err := h.b.remove(context.Background(), environment.TierProduction, nil); err != nil {
		t.Fatalf("remove() = %v", err)
	}

	if h.run.serving() != nil {
		t.Error("the bastion service outlived its tier")
	}
	deleted := h.run.identities().deletedAccounts
	if len(deleted) != 1 || !strings.Contains(deleted[0], bastionNames().BastionAccount(environment.TierProduction)) {
		t.Errorf("deleted accounts = %v, want the production bastion's", deleted)
	}
}

func TestTakingDownATierThatNeverForwardedAPortDeletesNothingAndFailsNothing(t *testing.T) {
	t.Parallel()
	h := newBastionHarness(t)

	if err := h.b.remove(context.Background(), environment.TierProduction, nil); err != nil {
		t.Fatalf("remove() = %v", err)
	}

	if len(h.run.identities().deletedAccounts) != 0 {
		t.Error("an account was deleted for a bastion never made")
	}
}

func destinationsOf(t *testing.T, targets ...string) []relay.Destination {
	t.Helper()
	destinations := make([]relay.Destination, len(targets))
	for i, target := range targets {
		address, err := netip.ParseAddrPort(target)
		if err != nil {
			t.Fatal(err)
		}
		destinations[i] = relay.Destination{Network: netip.PrefixFrom(address.Addr(), address.Addr().BitLen()), Port: address.Port()}
	}
	return destinations
}

var errTestUserLogin = errors.New("this credential is a user's own login")

func echoTarget(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener.Addr().String()
}

func echoed(t *testing.T, address, message string) string {
	t.Helper()
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatalf("dial %s: %v", address, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte(message)); err != nil {
		t.Fatalf("write to %s: %v", address, err)
	}
	got := make([]byte, len(message))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read from %s: %v", address, err)
	}
	return string(got)
}
