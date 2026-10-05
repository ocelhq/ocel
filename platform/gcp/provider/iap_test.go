package gcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"google.golang.org/api/googleapi"
	iap "google.golang.org/api/iap/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

func TestAPreviewWithNoEdgeInFrontIsServedBehindIdentityAwareProxy(t *testing.T) {
	p := pushing(t, "")

	if !p.servesBehindIAP(previewSpec()) {
		t.Error("a preview with no edge in front is served to anyone with its run.app url, want it behind Identity-Aware Proxy")
	}

	production := previewSpec()
	production.Ref.Tier = environment.TierProduction
	production.Ref.Name = naming.StackName{Env: stackrecords.ProductionEnv, App: "web"}
	if p.servesBehindIAP(production) {
		t.Error("a production release is behind Identity-Aware Proxy, and production answers anyone")
	}

	shielded := previewSpec()
	front, err := p.Edges().Open(alb.Kind, nil)
	if err != nil {
		t.Fatal(err)
	}
	shielded.Edge = front
	if p.servesBehindIAP(shielded) {
		t.Error("a preview behind the load balancer is behind Identity-Aware Proxy on its service too, and Cloud Run refuses IAP on both")
	}

	if pushing(t, "http://127.0.0.1:4588").servesBehindIAP(previewSpec()) {
		t.Error("a preview against the emulator is behind Identity-Aware Proxy, which the emulator does not serve")
	}
}

func TestAServiceBehindIdentityAwareProxyChecksItsInvokerSoOnlyTheProxyReachesIt(t *testing.T) {
	desired := desiredOf(t, serving{service: "ocel-shop-pr-7-web", compute: provider.ComputeContainer, health: "/", iap: true})

	if !desired.IapEnabled {
		t.Error("the service has Identity-Aware Proxy off")
	}
	if desired.InvokerIamDisabled {
		t.Error("the service skips its invoker check, so the proxy's own grant is all that stands between it and the internet")
	}
}

func TestThePreviewViewersAreTheOnlyMembersTheProxyAdmits(t *testing.T) {
	current := &iap.Policy{Etag: "BwX", Bindings: []*iap.Binding{
		{Role: viewerRole, Members: []string{"user:gone@example.com"}},
		{Role: "roles/iap.admin", Members: []string{"user:admin@example.com"}},
	}}

	admitted := viewerPolicy(current, []string{"group:team@example.com", "user:ana@example.com"})

	var viewers []string
	for _, binding := range admitted.Bindings {
		if binding.Role == viewerRole {
			viewers = append(viewers, binding.Members...)
		}
	}
	if want := []string{"group:team@example.com", "user:ana@example.com"}; !slices.Equal(viewers, want) {
		t.Errorf("the proxy admits %v, want exactly %v: a viewer taken out of previewViewers loses access", viewers, want)
	}
	if !slices.ContainsFunc(admitted.Bindings, func(binding *iap.Binding) bool { return binding.Role == "roles/iap.admin" }) {
		t.Error("admitting viewers dropped a binding of another role")
	}
	if admitted.Etag != "BwX" {
		t.Errorf("the policy is written over etag %q, want the %q it was read at", admitted.Etag, "BwX")
	}

	none := viewerPolicy(current, nil)
	if slices.ContainsFunc(none.Bindings, func(binding *iap.Binding) bool { return binding.Role == viewerRole }) {
		t.Error("with no viewers named the proxy still admits someone")
	}
}

func TestAPreviewViewerThatNamesNoKindOfMemberIsRefused(t *testing.T) {
	err := refuseViewersWithoutKind([]string{"user:ana@example.com", "ana@example.com"})

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("refuseViewersWithoutKind = %v, want an %s refusal: IAM takes a member only with its kind in front", err, refusal.CodeInvalid)
	}
	if err := refuseViewersWithoutKind([]string{"user:ana@example.com", "group:team@example.com", "domain:example.com"}); err != nil {
		t.Errorf("refuseViewersWithoutKind of members IAM takes = %v", err)
	}
}

func TestAPreviewBehindIdentityAwareProxyLetsViewersInOnlyWhileAPointerPinsIt(t *testing.T) {
	server := &runServer{}
	preview := serves("ocel-shop-pr-7-web")
	preview.iap = true
	preview.opensOnPromotion = true
	_, one := released(t, server, preview)

	if server.serving().IapEnabled || server.serving().InvokerIamDisabled {
		t.Fatal("a preview its first release created lets viewers in before a promotion pinned it")
	}
	p := server.open(t)
	opened, err := p.Pin(context.Background(), preview.service, one, nil)
	if err != nil || !opened {
		t.Fatalf("Pin(%s) = %v, %v, want it to open the preview", one, opened, err)
	}
	if !server.serving().IapEnabled || server.serving().InvokerIamDisabled {
		t.Errorf("the promoted preview has the proxy %v and the invoker check skipped %v, want the proxy on and the invoker check kept",
			server.serving().IapEnabled, server.serving().InvokerIamDisabled)
	}

	second := preview
	second.image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	released(t, server, second)
	if !server.serving().IapEnabled {
		t.Error("a later release turned the proxy off, and every viewer would be refused until the next promotion")
	}

	if err := p.Close(context.Background(), preview.service, nil); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if server.serving().IapEnabled {
		t.Error("a closed preview still lets its viewers in through the proxy, and the router says a removed pointer is served no more")
	}
}

func TestAnOpenPreviewReleasedBehindTheLoadBalancerTurnsTheProxyOffAndAnswersEveryone(t *testing.T) {
	server := &runServer{}
	preview := serves("ocel-shop-pr-7-web")
	preview.iap = true
	preview.opensOnPromotion = true
	_, one := released(t, server, preview)
	if _, err := server.open(t).Pin(context.Background(), preview.service, one, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", one, err)
	}

	behind := serves("ocel-shop-pr-7-web")
	behind.opensOnPromotion = true
	behind.ingress = ingressLoadBalancer
	released(t, server, behind)

	if server.serving().IapEnabled || !server.serving().InvokerIamDisabled {
		t.Errorf("the open preview released behind the load balancer has the proxy %v and the invoker check skipped %v, "+
			"want the proxy off and the invoker check skipped: the proxy cannot be on both the service and the load balancer",
			server.serving().IapEnabled, server.serving().InvokerIamDisabled)
	}
}

func TestAnOpenServiceReleasedAsAPreviewWithNoEdgeTurnsTheProxyOn(t *testing.T) {
	server := &runServer{}
	app := promotable("ocel-shop-pr-7-web")
	_, one := released(t, server, app)
	if _, err := server.open(t).Pin(context.Background(), app.service, one, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", one, err)
	}

	gated := app
	gated.iap = true
	released(t, server, gated)

	if !server.serving().IapEnabled || server.serving().InvokerIamDisabled {
		t.Errorf("the open service released behind the proxy has the proxy %v and the invoker check skipped %v, "+
			"want the proxy on and the invoker check kept: otherwise the preview answers everyone",
			server.serving().IapEnabled, server.serving().InvokerIamDisabled)
	}
}

func TestAPreviewBootstrapNeedsTheProxyTurnedOnAndCreatesItsServiceAgent(t *testing.T) {
	if !slices.Contains(apisFor(environment.TierPreview, nil), proxyAPI) {
		t.Errorf("a preview bootstrap checks %v, want %s among them: a preview with no edge in front is served behind the proxy", apisFor(environment.TierPreview, nil), proxyAPI)
	}
	names := Names{namespace: "ocel", project: "acme-prod"}
	agent := item{Kind: KindServiceAgent, Name: proxyAPI}
	if !slices.ContainsFunc(stackItems(names, environment.TierPreview, false), func(each item) bool { return each.ID() == agent.ID() }) {
		t.Error("a preview bootstrap does not create the proxy's service agent, and a preview cannot let the proxy call its service until it exists")
	}
	if slices.ContainsFunc(stackItems(names, environment.TierPreview, true), func(each item) bool { return each.ID() == agent.ID() }) {
		t.Error("a bootstrap against the emulator creates the proxy's service agent, and the emulator serves no proxy")
	}
}

func TestAProductionBootstrapNeedsNeitherTheProxyNorItsServiceAgent(t *testing.T) {
	if slices.Contains(apisFor(environment.TierProduction, nil), proxyAPI) {
		t.Errorf("a production bootstrap checks %v, want %s left out: only a preview is served behind the proxy", apisFor(environment.TierProduction, nil), proxyAPI)
	}
	names := Names{namespace: "ocel", project: "acme-prod"}
	if slices.ContainsFunc(stackItems(names, environment.TierProduction, false), func(each item) bool { return each.Kind == KindServiceAgent }) {
		t.Error("a production bootstrap creates the proxy's service agent, and nothing in production is served behind the proxy")
	}
}

func TestAPreviewBehindTheProxyIsRefusedUntilABootstrapRecordsItsServiceAgent(t *testing.T) {
	recorded := stamp{State: stateComplete, Agents: []string{proxyAPI}}
	if err := refuseMissingProxyAgent(environment.TierPreview, recorded); err != nil {
		t.Errorf("refuseMissingProxyAgent(agent recorded) = %v", err)
	}

	for name, written := range map[string]stamp{
		"no agent recorded":           {State: stateComplete},
		"a bootstrap still applying":  {State: stateApplying, Agents: []string{proxyAPI}},
		"no bootstrap written at all": {},
	} {
		err := refuseMissingProxyAgent(environment.TierPreview, written)
		var refused refusal.Refusal
		if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady || !strings.Contains(err.Error(), "ocel bootstrap preview") {
			t.Errorf("refuseMissingProxyAgent(%s) = %v, want it refused naming the bootstrap that creates the agent", name, err)
		}
	}
}

func TestABootstrapWithItsServiceAgentRecordedSurveysTheAgentAsPresent(t *testing.T) {
	if (stamp{State: stateComplete}).hasAgent(proxyAPI) {
		t.Error("a stamp that records no agent says the proxy's agent was created, so a bootstrap would never try again")
	}
	if !(stamp{State: stateComplete, Agents: []string{proxyAPI}}).hasAgent(proxyAPI) {
		t.Error("a complete stamp recording the proxy's agent says it was not created")
	}
}

type identityServer struct {
	asked  []string
	status int
}

func (s *identityServer) open(t *testing.T) *clients {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.asked = append(s.asked, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if s.status != 0 {
			w.WriteHeader(s.status)
			_, _ = fmt.Fprintf(w, `{"error":{"code":%d,"message":%q}}`, s.status, http.StatusText(s.status))
			return
		}
		_, _ = w.Write([]byte(`{"name":"operations/agent","done":true}`))
	}))
	t.Cleanup(server.Close)
	return &clients{Names: Names{namespace: "ocel", project: "acme-prod"}, region: "europe-west1", endpoint: server.URL, projectNumber: 123}
}

func TestCreatingTheProxyServiceAgentAsksServiceUsageThroughTheProviderClient(t *testing.T) {
	server := &identityServer{}
	b := bootstrap{clients: server.open(t)}

	if err := b.makeServiceAgent(context.Background(), proxyAPI); err != nil {
		t.Fatalf("makeServiceAgent = %v", err)
	}

	want := "POST /v1beta1/projects/123/services/iap.googleapis.com:generateServiceIdentity"
	if !slices.Contains(server.asked, want) {
		t.Errorf("creating the agent asked %v, want %s through the client every other call uses, which carries the credential's quota project", server.asked, want)
	}
}

func TestAServiceAgentThatCannotBeCreatedFailsTheBootstrap(t *testing.T) {
	server := &identityServer{status: http.StatusForbidden}
	c := server.open(t)
	b := bootstrap{clients: c}

	err := b.provision(context.Background(), survey{Names: c.Names, Project: c.project}, item{Kind: KindServiceAgent, Name: proxyAPI}, progress.Discard())

	if err == nil {
		t.Error("provision of an agent Service Usage refused = nil, want the bootstrap to fail: a bootstrap that completes says the agent exists, and nothing would try again")
	}
}

func TestOnlyAForbiddenServiceAgentCreationAsksForAnOwner(t *testing.T) {
	forbidden := &identityServer{status: http.StatusForbidden}
	err := bootstrap{clients: forbidden.open(t)}.makeServiceAgent(context.Background(), proxyAPI)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeDenied || !strings.Contains(err.Error(), "owner of project") {
		t.Errorf("makeServiceAgent refused with 403 = %v, want a %s refusal asking for an owner", err, refusal.CodeDenied)
	}

	invalid := &identityServer{status: http.StatusBadRequest}
	err = bootstrap{clients: invalid.open(t)}.makeServiceAgent(context.Background(), proxyAPI)
	var answered *googleapi.Error
	if err == nil || errors.As(err, &refused) || !errors.As(err, &answered) || answered.Code != http.StatusBadRequest {
		t.Errorf("makeServiceAgent refused with 400 = %v, want Google's answer passed through rather than a permission refusal: an owner would hit the same error", err)
	}
}

func TestAPreviewNamingAnEdgeThisProviderCannotOpenIsRefusedAtPreflight(t *testing.T) {
	p := &Provider{options: Options{Project: "acme-prod", Region: "europe-west1"}, namespace: "ocel"}

	_, err := p.proxiesPreviews(provider.DeployPreflight{Deploy: provider.DeploySpec{Tier: environment.TierPreview}, Edge: "nonsense"})

	if err == nil {
		t.Error("proxiesPreviews of an edge the provider cannot open = nil, want the refusal opening it gave: " +
			"otherwise the preflight lets the preview through as if nothing served it behind the proxy")
	}
}

func TestRemovingAPreviewTierWhoseStampRecordsTheProxyAgentKeepsTheAgent(t *testing.T) {
	names := Names{namespace: "ocel", project: "acme-prod"}
	tier := environment.TierPreview
	read := survey{Tier: tier, Names: names, present: map[string]bool{}}
	for _, each := range bootstrapItems(names, tier, false) {
		read.present[each.ID()] = true
	}

	agent := item{Kind: KindServiceAgent, Name: proxyAPI}
	for _, taking := range removals(read) {
		if taking.item.ID() != agent.ID() {
			continue
		}
		if taking.action != provider.ActionKeep || taking.reason != reasonAgentKept {
			t.Errorf("removal of the proxy's service agent is %s (%q), want it kept because %q: Google offers no call that deletes a service agent, so a removal that tries fails after the stamp already reads as removing",
				taking.action, taking.reason, reasonAgentKept)
		}
		return
	}
	t.Error("a preview removal says nothing of the proxy's service agent, and the plan should say it is kept and why")
}

func TestTheProxyAdmitsTheAccountThePreviewRunsAsBesideTheViewersSoAPromotionCanWarmThePreview(t *testing.T) {
	admitted := admittedMembers("ocel-1a2b3c4d5e@acme-prod.iam.gserviceaccount.com", []string{"user:ana@example.com"})

	want := []string{"serviceAccount:ocel-1a2b3c4d5e@acme-prod.iam.gserviceaccount.com", "user:ana@example.com"}
	if !slices.Equal(slices.Sorted(slices.Values(admitted)), want) {
		t.Errorf("the proxy admits %v, want %v: a promotion warms a preview with a token signed as that account", admitted, want)
	}
}

func TestAPreviewBehindIdentityAwareProxyTagsTheRevisionItsReleaseCreated(t *testing.T) {
	p := pushing(t, "")
	preview := previewSpec()
	preview.Ref.Name.Release = naming.NewRelease("d1", "f1")

	if got, want := p.revisionTag(preview), preview.Ref.Name.Release.String(); got != want {
		t.Errorf("revisionTag() = %q, want %q: each preview deployment is answered on its own revision's tag, behind the service's proxy", got, want)
	}

	production := preview
	production.Ref.Tier = environment.TierProduction
	if got := p.revisionTag(production); got != "" {
		t.Errorf("revisionTag() of a production release with no edge in front = %q, want none: the service answers anyone, so a tag would publish every unpromoted revision", got)
	}
}
