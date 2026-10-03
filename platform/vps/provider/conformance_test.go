package vps_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func TestTheDNSRegistryOpensACloudflareWriter(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "conformance")

	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})
	registry := p.DNS()

	conformance.RunDNS(t, p.Facts(), registry)
	if got := p.Facts().DNSKinds; !slices.Equal(got, []provider.DNSKind{"cloudflare"}) {
		t.Errorf("Facts().DNSKinds = %v, want cloudflare alone", got)
	}
	writer, err := registry.Open("cloudflare", "app.com", "")
	if err != nil || writer == nil {
		t.Fatalf("Open(cloudflare) = %v, %v, want a writer", writer, err)
	}

	var rejection refusal.Refusal
	opened, err := registry.Open("route53", "app.com", "")
	if !errors.As(err, &rejection) || rejection.Code != refusal.CodeInvalid {
		t.Fatalf("Open(route53) = %v, %v, want an invalid refusal", opened, err)
	}
	if !strings.Contains(rejection.Message, "cloudflare") {
		t.Errorf("Open(route53) refusal = %q, want it to name the writers this provider has", rejection.Message)
	}
}

func TestAProjectThatNamesNoEdgeIsAnsweredByTheSwitchboardOnTheBox(t *testing.T) {
	t.Parallel()

	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})
	registry := p.Edges()

	conformance.RunEdges(t, p.Facts(), registry)

	if got := p.Facts().Edges; !slices.Equal(got, []edge.Kind{"cloudflare"}) {
		t.Errorf("Facts().Edges = %v, want cloudflare alone: the proxy on the box answers a hostname, and it is no edge", got)
	}
	if got := p.Facts().DefaultEdge; got != edge.None {
		t.Errorf("Facts().DefaultEdge = %q, want no edge: a deploy that names none reaches the box's own proxy", got)
	}
	if got := p.Facts().ListPairedRouters(edge.None); !slices.Equal(got, []router.Kind{switchboard.RouterKind}) {
		t.Errorf("ListPairedRouters(no edge) = %v, want the switchboard alone", got)
	}

	for _, named := range []edge.Kind{"cloudfront", "box"} {
		var rejection refusal.Refusal
		opened, err := registry.Open(named, nil)
		if !errors.As(err, &rejection) || rejection.Code != refusal.CodeInvalid {
			t.Fatalf("Open(%s) = %v, %v, want an invalid refusal", named, opened, err)
		}
		if !strings.Contains(rejection.Message, string(named)) || !strings.Contains(rejection.Message, "leave `edge` out") {
			t.Errorf("Open(%s) refused with %q, want it to name the edge asked for and say to leave the edge out", named, rejection.Message)
		}
		if strings.Contains(strings.ToLower(rejection.Message), "rout") {
			t.Errorf("Open(%s) refused with %q; a router is never user-facing, so no refusal names one", named, rejection.Message)
		}
	}
}

func TestCloudflareFrontsTheBoxAsAProxyThatRunsNoCodeAndForwardsToTheSwitchboard(t *testing.T) {
	t.Parallel()

	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})
	front, err := p.Edges().Open("cloudflare", nil)
	if err != nil {
		t.Fatalf("Open(cloudflare) = %v, want the Cloudflare proxy", err)
	}
	if facts := front.Facts(); facts.RunsCode || !facts.ProxiesRecords || facts.ServesUnbound {
		t.Errorf("the cloudflare edge's facts = %+v, want a proxy that forwards records to an origin and runs no worker", facts)
	}
	if front.Hooks().ClientCertificates == nil {
		t.Error("the cloudflare edge presents no client certificate, so the box could not refuse a request that did not come through it")
	}
	if got := p.Facts().ListPairedRouters("cloudflare"); !slices.Equal(got, []router.Kind{switchboard.RouterKind}) {
		t.Errorf("ListPairedRouters(cloudflare) = %v, want the switchboard alone", got)
	}
}

func TestTheRouterRegistryOpensTheRouterEveryEdgePairsWith(t *testing.T) {
	t.Parallel()

	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})
	conformance.RunRouters(t, p.Facts(), p.Edges(), p.Routers())
}

func TestVPSProvider(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "conformance")

	conformance.Run(t, conformance.Suite{
		Server:  providerserver.Config{Version: "test", New: vps.New},
		Options: provider.Options{"ssh": map[string]any{"host": "203.0.113.10"}},
		Binary:  buildProvider(t),
		Certificates: &conformance.CertificateChecks{
			Kind:      edge.None,
			Hostnames: []string{"shop.example.com", "www.shop.example.com"},
			Handle:    certs.ProxyHandle,
		},
	})
}

func TestTheProviderNamesTheVendorAndSetsTheHooksABoxImplements(t *testing.T) {
	t.Parallel()

	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})

	if p.Facts().Vendor != vps.Vendor {
		t.Errorf("Facts().Vendor = %q, want %q", p.Facts().Vendor, vps.Vendor)
	}
	if got := p.Facts().Bindings; !slices.Equal(got, []provider.BindingType{provider.BindingPostgres, provider.BindingBucket, provider.BindingKV, provider.BindingTopic, provider.BindingTask, provider.BindingRealtime}) {
		t.Errorf("Facts().Bindings = %v, want the binding types a box provisions for itself", got)
	}

	hooks := p.Hooks()
	for name, set := range map[string]bool{
		"WarmFunctions": hooks.WarmFunctions != nil,
		"EmbedCode":     hooks.EmbedCode != nil,
		"InspectStack":  hooks.InspectStack != nil,
		"VerifyGrants":  hooks.VerifyGrants != nil,
		"PackApp":       hooks.PackApp != nil,
		"ProveIdentity": hooks.ProveIdentity != nil,
	} {
		if set {
			t.Errorf("the box's hooks set %s, a step no box takes", name)
		}
	}
	if hooks.PreflightDeploy == nil {
		t.Error("the box's hooks leave PreflightDeploy nil, and a box then learns its engine, its disk, its proxy or its ports are not ready halfway through an image transfer")
	}
	if hooks.CheckHost == nil {
		t.Error("the box's hooks leave CheckHost nil, and `doctor` calls Preflight and DescribeBootstrap and nothing else: with no permanent port there is nowhere for a verdict about the life of the box to arrive")
	}
}

func TestTheReleasePortRefusesTheResourcesThisProviderServesNoneOf(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	release := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}}).Stacks()
	spec := provider.StackSpec{
		Ref: provider.StackRef{
			Project: "shop",
			Tier:    environment.TierProduction,
			Name:    naming.InfraStack("prod"),
		},
		Kind:      provider.StackInfra,
		Resources: []provider.Resource{{Name: "orders", Type: provider.BindingPostgres}},
	}

	var refused refusal.Refusal
	if _, err := release.Plan(ctx, spec, nil); !errors.As(err, &refused) {
		t.Errorf("Plan() of a resource this provider serves none of = %v, want a refusal", err)
	}
	if _, err := release.Provision(ctx, spec, nil); !errors.As(err, &refused) {
		t.Errorf("Provision() of a resource this provider serves none of = %v, want a refusal rather than a release that reads as done", err)
	}
}

func buildProvider(t *testing.T) string {
	t.Helper()
	if prebuilt := os.Getenv("OCEL_VPS_PROVIDER_BINARY"); prebuilt != "" {
		return prebuilt
	}
	binary := filepath.Join(t.TempDir(), "deploy")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, "./cmd/deploy")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the vps provider: %v\n%s", err, out)
	}
	return binary
}

func TestTheTopicsTasksAndWorkersABoxRunsAreOnesTheBuildCanPlace(t *testing.T) {
	t.Parallel()

	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})
	conformance.RunWorkers(t, p.Facts())
}

func TestKVStoresPassTheConformanceSuite(t *testing.T) {
	t.Parallel()

	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})
	conformance.RunKVStores(t, p.Facts())
}

func TestRealtimeIsRefusedAtPreflightAsUnsupported(t *testing.T) {
	t.Parallel()

	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})
	conformance.RunRealtime(t, p.Facts())
}

type dockerLogs struct {
	mu      sync.Mutex
	entries map[string][]provider.LogEntry
}

func (d *dockerLogs) seed(_ *testing.T, target provider.LogTarget, entries []provider.LogEntry) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.entries == nil {
		d.entries = map[string][]provider.LogEntry{}
	}
	d.entries[target.Container.Physical] = entries
}

func (d *dockerLogs) answer(command string) (session.Result, bool) {
	if !strings.Contains(command, "docker logs") {
		return session.Result{}, false
	}
	words := strings.Fields(strings.ReplaceAll(command, "'", ""))
	flag := func(name string) string {
		if at := slices.Index(words, name); at >= 0 {
			return words[at+1]
		}
		return ""
	}
	since, _ := time.Parse(time.RFC3339Nano, flag("--since"))
	until, _ := time.Parse(time.RFC3339Nano, flag("--until"))
	d.mu.Lock()
	defer d.mu.Unlock()
	logged := d.entries[words[len(words)-1]]
	if tail, err := strconv.Atoi(flag("--tail")); err == nil {
		logged = logged[len(logged)-min(len(logged), tail):]
	}
	var kept []provider.LogEntry
	for _, entry := range logged {
		if !until.IsZero() && entry.Time.After(until) {
			break
		}
		if !entry.Time.Before(since) {
			kept = append(kept, entry)
		}
	}
	var stdout strings.Builder
	for _, entry := range kept {
		stdout.WriteString(entry.Time.Format(time.RFC3339Nano) + " " + entry.Message + "\n")
	}
	return session.Result{Stdout: stdout.String()}, true
}

func TestTheLogsPortKeepsTheConformanceHistoryOfAContainerOnTheBox(t *testing.T) {
	t.Parallel()

	docker := &dockerLogs{}
	p := over(&box{refuses: docker.answer})
	conformance.RunLogs(t, p.Facts(), p.Logs())
	conformance.RunLogHistory(t, p.Facts(), p.Logs(), docker.seed)
}
