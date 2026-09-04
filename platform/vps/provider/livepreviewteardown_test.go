package vps_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	boxedge "github.com/ocelhq/ocel/platform/vps/provider/box"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const (
	teardownSlug = "teardown"
	teardownApp  = "front"
	teardownRepo = "ocel-live-teardown"
	liveIssuer   = "acme-v02.api.letsencrypt.org-directory"
)

func teardownAt(pointer string) string { return teardownRepo + ":" + pointer }

func onABoxServingPreviews(t *testing.T) (machine, *vps.Provider, edge.EdgeStack) {
	t.Helper()

	vm := live(t)
	bootstrapped(t, vm, providerkit.ClassProduction)
	bootstrapped(t, vm, providerkit.ClassPreview)
	fixtures(t, vm)
	t.Cleanup(func() {
		vm.ssh(t, "sudo docker ps -aq --filter label="+host.LabelApp+"="+teardownApp+" | xargs -r sudo docker rm -f >/dev/null 2>&1 || true")
		vm.ssh(t, "sudo docker images -q --filter reference="+teardownRepo+":* | xargs -r sudo docker rmi -f >/dev/null 2>&1 || true")
		vm.ssh(t, "sudo rm -rf "+host.ReleasesDir()+"/"+teardownSlug)
	})

	p := vm.deploying(t)
	front, err := p.Edges().Open(boxedge.Kind)
	if err != nil {
		t.Fatalf("Open(%q) = %v", boxedge.Kind, err)
	}
	if _, err := front.ReconcilePreviewWildcard(context.Background(), edge.PreviewWildcardSpec{
		BaseDomain: livePreviewBase,
		GrammarMin: edge.PreviewGrammarMin,
		GrammarMax: edge.PreviewGrammarMax,
	}); err != nil {
		t.Fatalf("ReconcilePreviewWildcard: %v", err)
	}
	stack, err := front.Reconcile(context.Background(), edge.StackSpec{
		Version: "test", Class: edge.ClassPreview, Slug: teardownSlug,
	}, edge.StackState{GlobalPreview: livePreviewBase})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	t.Cleanup(func() {
		if err := stack.Destroy(context.Background()); err != nil {
			t.Errorf("Destroy: %v", err)
		}
		if err := front.DestroyPreviewWildcard(context.Background(), livePreviewBase); err != nil {
			t.Errorf("DestroyPreviewWildcard: %v", err)
		}
	})
	return vm, p, stack
}

func teardownHostname(pointer string) string {
	return edge.SharedPreview(teardownSlug, livePreviewBase).Hosts(pointer, []string{teardownApp})[0]
}

func previewBuild(t *testing.T, pointer string) providerkit.Build {
	t.Helper()

	sum := sha256.Sum256([]byte(pointer))
	build, err := providerkit.NewBuild(hex.EncodeToString(sum[:])[:32], pointer, "")
	if err != nil {
		t.Fatal(err)
	}
	return build
}

func previewRef(t *testing.T, pointer string) providerkit.StackRef {
	t.Helper()

	return providerkit.StackRef{
		Project: teardownSlug,
		Class:   providerkit.ClassPreview,
		Name:    naming.AppStack(pointer, teardownApp, previewBuild(t, pointer).Release()),
	}
}

func (vm machine) ships(t *testing.T, pointer string) {
	t.Helper()

	if strings.TrimSpace(vm.ssh(t, "sudo docker image inspect "+teardownAt(pointer)+" >/dev/null 2>&1 && echo held || echo gone")) == "held" {
		return
	}
	vm.feeds(t, "sudo docker build -q -t "+teardownAt(pointer)+" - >/dev/null",
		[]byte("FROM "+fixtureBase+"\nENV RELEASE="+pointer+"\n"))
}

func previewUp(t *testing.T, vm machine, p *vps.Provider, stack edge.EdgeStack, pointer string, at int64) {
	t.Helper()

	vm.ships(t, pointer)
	ctx := context.Background()
	build := previewBuild(t, pointer)
	plan := providerkit.StackPlan{
		Ref:  previewRef(t, pointer),
		Kind: providerkit.StackApp,
		App: &providerkit.AppPlan{
			App:             teardownApp,
			Compute:         providerkit.ComputeContainer,
			Deployment:      build.DeploymentID(),
			Image:           teardownAt(pointer),
			HealthCheckPath: healthPath,
		},
	}
	stood, err := resources.Releaser(p.Records(), p.Artifacts(), p).Provision(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Provision(%s) = %v", pointer, err)
	}
	if len(stood.Containers) != 1 {
		t.Fatalf("Provision(%s) stood up %v", pointer, stood.Containers)
	}
	if err := providerkit.WriteStack(ctx, p.Records(), providerkit.ClassPreview, teardownSlug, plan.Ref.Name, providerkit.Stack{
		Kind:       providerkit.StackApp,
		App:        teardownApp,
		Release:    build.Release().String(),
		Identity:   build.String(),
		Containers: stood.Containers,
		Writer:     providerkit.WriterFor(""),
	}); err != nil {
		t.Fatalf("WriteStack(%s): %v", pointer, err)
	}
	if err := stack.Ledger().PutStaged(ctx, edge.DeploymentRecord{
		App:        teardownApp,
		Identity:   build.String(),
		Entry:      "/",
		Image:      teardownAt(pointer),
		Physical:   stood.Containers[0].Physical,
		HealthPath: healthPath,
	}); err != nil {
		t.Fatalf("PutStaged(%s): %v", pointer, err)
	}
	if err := stack.Promote(ctx, edge.Promotion{
		PromotionID: "p-" + pointer, Ts: at, Builds: map[string]string{teardownApp: build.String()},
	}, pointer, edge.DiscardReporter()); err != nil {
		t.Fatalf("Promote(%s): %v", pointer, err)
	}
}

func previewRemove(t *testing.T, p *vps.Provider, stack edge.EdgeStack, pointer string) *said {
	t.Helper()

	ctx := context.Background()
	spoken := &said{}
	removed, err := stack.RemovePointer(ctx, pointer, spoken)
	if err != nil {
		t.Fatalf("RemovePointer(%s) = %v", pointer, err)
	}
	if err := providerkit.ReclaimPreview(ctx, p, teardownSlug, pointer, removed, spoken); err != nil {
		t.Fatalf("ReclaimPreview(%s) = %v", pointer, err)
	}
	infra := providerkit.StackRef{Project: teardownSlug, Class: providerkit.ClassPreview, Name: naming.InfraStack(pointer)}
	if err := p.Releases().Destroy(ctx, infra, spoken); err != nil {
		t.Fatalf("Destroy(%s) = %v: an ephemeral preview stands up no infra stack, and teardown destroys one regardless", infra.Name, err)
	}
	return spoken
}

func (vm machine) plants(t *testing.T, hostname string) string {
	t.Helper()

	held := host.ProxyData + "/caddy/certificates/" + liveIssuer + "/" + hostname
	vm.ssh(t, "sudo install -d -m 700 "+quote(held))
	for _, suffix := range []string{".crt", ".key", ".json"} {
		vm.ssh(t, "printf %s "+quote(hostname)+" | sudo tee "+quote(held+"/"+hostname+suffix)+" >/dev/null")
	}
	return held
}

func (vm machine) certificates(t *testing.T) string {
	t.Helper()

	return vm.ssh(t, "sudo find "+quote(host.ProxyData+"/caddy/certificates")+" -mindepth 2 -maxdepth 2 -type d -name "+
		quote(teardownSlug+"--*")+" -printf '%f\\n' 2>/dev/null | sort")
}

func (vm machine) teardownImages(t *testing.T) string {
	t.Helper()

	return strings.TrimSpace(vm.ssh(t,
		"sudo docker images --filter reference="+teardownRepo+":* --format '{{.Repository}}:{{.Tag}}' | sort"))
}

func (vm machine) routedHosts(t *testing.T) []string {
	t.Helper()

	routes, held := nestedIn(t, vm.loadedProxyConfig(t), "apps", "http", "servers", "ocel", "routes").([]any)
	if !held {
		t.Fatal("the loaded configuration carries no routes at all")
	}
	written, err := json.Marshal(routes)
	if err != nil {
		t.Fatal(err)
	}
	var read []struct {
		Match []struct {
			Host []string `json:"host"`
		} `json:"match"`
	}
	if err := json.Unmarshal(written, &read); err != nil {
		t.Fatal(err)
	}
	var hosts []string
	for _, route := range read {
		for _, match := range route.Match {
			hosts = append(hosts, match.Host...)
		}
	}
	return hosts
}

func TestLiveAPreviewTornDownLeavesNoRouteNoCertificateAndNoImageBehind(t *testing.T) {
	vm, p, stack := onABoxServingPreviews(t)

	previewUp(t, vm, p, stack, "pr-7", 1)
	hostname := teardownHostname("pr-7")
	planted := vm.plants(t, hostname)

	if !slices.Contains(vm.routedHosts(t), hostname) {
		t.Fatalf("the proxy loaded no route for %s after the preview went up, so this teardown has nothing to take: %v", hostname, vm.routedHosts(t))
	}
	if !strings.Contains(vm.certificates(t), hostname) {
		t.Fatalf("%s is not in the proxy's store, and this test needs the pair a box obtains for every preview hostname it terminates", planted)
	}
	if !strings.Contains(vm.teardownImages(t), teardownAt("pr-7")) {
		t.Fatalf("the box holds no image for the preview it is serving: %q", vm.teardownImages(t))
	}

	previewRemove(t, p, stack, "pr-7")

	if routed := vm.routedHosts(t); slices.Contains(routed, hostname) {
		t.Errorf("the proxy still routes %s after its preview came down: %v", hostname, routed)
	}
	if held := vm.certificates(t); strings.Contains(held, hostname) {
		t.Errorf("the proxy's store still holds a subject for %s:\n%s\nA pair per preview hostname ever served is a retention term that grows with previews-ever, and a teardown that leaves bytes behind fails that on its own", hostname, held)
	}
	if held := vm.teardownImages(t); strings.Contains(held, teardownAt("pr-7")) {
		t.Errorf("the box still holds %s: %q. The sweep is a deploy's final act, and this box may never be deployed to again", teardownAt("pr-7"), held)
	}
	answered := vm.peers(t, "curl -sS -m 10 -o /dev/null -D - -H "+quote("Host: "+hostname)+" http://"+host.ProxyContainer+"/")
	if !strings.Contains(answered, "404") ||
		!strings.Contains(strings.ToLower(answered), strings.ToLower(host.EdgeHeader)+": "+host.EdgeName) {
		t.Errorf("%s was answered\n%s\nafter its preview came down, want the catch-all's 404 carrying %s: %s. A 404 from a route this teardown was meant to remove reads the same on the status line alone",
			hostname, answered, host.EdgeHeader, host.EdgeName)
	}
	if wildcard := edge.PreviewWildcard(livePreviewBase); !slices.Contains(vm.routedHosts(t), wildcard) {
		t.Errorf("the catch-all %s went down with one project's preview, and it is a bootstrap item answering for every project this box serves: %v", wildcard, vm.routedHosts(t))
	}
}

func TestLiveTearingDownAPreviewTwiceRefusesNothingAndTakesNothingMore(t *testing.T) {
	vm, p, stack := onABoxServingPreviews(t)

	previewUp(t, vm, p, stack, "pr-7", 1)
	vm.plants(t, teardownHostname("pr-7"))
	previewRemove(t, p, stack, "pr-7")
	settled := vm.certificates(t) + "\n" + vm.teardownImages(t)

	previewRemove(t, p, stack, "pr-7")

	if again := vm.certificates(t) + "\n" + vm.teardownImages(t); again != settled {
		t.Errorf("a second teardown left %q, want %q: `ocel preview rm` is retried on every failure, and a destroy that refuses a stack with nothing behind it strands whatever was reclaimed before it", again, settled)
	}
}

func TestLiveTearingDownOneOfFourLivePreviewsSweepsNoLivePreviewsImage(t *testing.T) {
	vm, p, stack := onABoxServingPreviews(t)

	for at, pointer := range []string{"pr-1", "pr-2", "pr-3", "pr-4"} {
		previewUp(t, vm, p, stack, pointer, int64(at)+1)
	}
	held := windowOf(t, vm, teardownSlug, teardownApp, providerkit.ClassPreview)
	if len(held) != 3 {
		t.Fatalf("the box's preview window reads %v, and this test turns on it being full: past the third live preview of one app the container's ocel.ref label is the sole guard against sweeping a live one", held)
	}
	if slices.Contains(held, teardownAt("pr-1")) {
		t.Fatalf("the window still names %s after four previews of one app, so no live preview here is guarded by its label alone and the regression this test exists for cannot happen: %v", teardownAt("pr-1"), held)
	}

	previewRemove(t, p, stack, "pr-2")

	standing := vm.teardownImages(t)
	for _, pointer := range []string{"pr-1", "pr-3", "pr-4"} {
		if !strings.Contains(standing, teardownAt(pointer)) {
			t.Errorf("%s went with another branch's teardown and %s is still up: %q", teardownAt(pointer), pointer, standing)
		}
	}
	if strings.Contains(standing, teardownAt("pr-2")) {
		t.Errorf("the teardown left %s on the box: %q", teardownAt("pr-2"), standing)
	}
}
