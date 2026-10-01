package vps_test

import (
	"context"
	"errors"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
)

type warned struct {
	mu    sync.Mutex
	lines []string
}

func (w *warned) Say(string) {}

func (w *warned) Warn(message string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lines = append(w.lines, message)
}

func (w *warned) Error(string) {}

func (w *warned) Detail(string) {}

func (w *warned) Debug(string) {}

func (w *warned) Span(string, time.Time, time.Time, error, ...progress.Attr) {}

var (
	filtered = &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}
	rejected = &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}
)

func reachingWith(causes map[string]error, dialed *[]string) vps.Reach {
	var mu sync.Mutex
	return func(_ context.Context, address string) error {
		mu.Lock()
		*dialed = append(*dialed, address)
		mu.Unlock()
		return causes[address]
	}
}

func preflightOver(machine *scripted, kind edge.Kind, reach vps.Reach) ([]string, error) {
	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (host.Conn, error) { return machine, nil },
	)
	p.Reaching(reach)
	stack, err := naming.ParseStackName("prod--web--r0a1b2c3d")
	if err != nil {
		return nil, err
	}
	log := &warned{}
	err = p.PreflightDeploy(context.Background(), provider.DeployPreflight{
		Deploy: provider.DeploySpec{
			Slug: "shop",
			Tier: environment.TierProduction,
			Apps: []provider.AppEntry{{App: "web", Stack: stack, Image: deployedRef}},
		},
		Edge:     kind,
		Progress: log,
	})
	return log.lines, err
}

func TestADeployOntoABoxWhosePortsAreFilteredFromOutsideGoesAheadAndNamesTheCloudFirewall(t *testing.T) {
	t.Parallel()

	var dialed []string
	warnings, err := preflightOver(boxSaying(nil), edge.None, reachingWith(map[string]error{
		"box.invalid:80": filtered, "box.invalid:443": filtered,
	}, &dialed))
	if err != nil {
		t.Fatalf("PreflightDeploy() = %v, want the deploy let through: its containers run either way, and a firewall rule opened later serves them", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("PreflightDeploy() warned %q, want one warning that the box's hostnames cannot be reached from outside", warnings)
	}
	for _, wanted := range []string{"box.invalid", "ports 80 and 443", "timed out", "listens", "firewall", "GCP", "security group"} {
		if !strings.Contains(warnings[0], wanted) {
			t.Errorf("warning = %q, want %q in it", warnings[0], wanted)
		}
	}
}

func TestAPortRefusedFromOutsideIsToldApartFromAPortThatTimesOut(t *testing.T) {
	t.Parallel()

	var dialed []string
	warnings, err := preflightOver(boxSaying(nil), edge.None, reachingWith(map[string]error{
		"box.invalid:80": rejected, "box.invalid:443": filtered,
	}, &dialed))
	if err != nil {
		t.Fatalf("PreflightDeploy() = %v, want the deploy let through with a warning", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("PreflightDeploy() warned %q, want one warning", warnings)
	}
	lines := strings.Split(warnings[0], "\n")
	about := func(port string) string {
		at := slices.IndexFunc(lines, func(line string) bool { return strings.Contains(line, "port "+port+":") })
		if at < 0 {
			t.Fatalf("warning = %q, want a line about port %s", warnings[0], port)
		}
		return lines[at]
	}
	if line := about("80"); !strings.Contains(line, "refused") || strings.Contains(line, "timed out") {
		t.Errorf("port 80's line = %q, want it refused and not timed out: a refusal is an active reject, a timeout a silent drop", line)
	}
	if line := about("443"); !strings.Contains(line, "timed out") || strings.Contains(line, "refused") {
		t.Errorf("port 443's line = %q, want it timed out and not refused", line)
	}
}

func TestAPortWindowsRefusesIsToldAsRefusedRatherThanByItsRawError(t *testing.T) {
	t.Parallel()

	var dialed []string
	refusedOnWindows := &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connectex", Err: syscall.Errno(10061)}}
	warnings, err := preflightOver(boxSaying(nil), edge.None, reachingWith(map[string]error{
		"box.invalid:80": refusedOnWindows,
	}, &dialed))
	if err != nil {
		t.Fatalf("PreflightDeploy() = %v, want the deploy let through with a warning", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "port 80: refused") {
		t.Fatalf("PreflightDeploy() warned %q, want port 80 told as refused", warnings)
	}
}

func TestADeployOntoABoxThatAnswersOnBothPortsFromOutsideWarnsNothing(t *testing.T) {
	t.Parallel()

	var dialed []string
	warnings, err := preflightOver(boxSaying(nil), edge.None, reachingWith(nil, &dialed))
	if err != nil {
		t.Fatalf("PreflightDeploy() = %v, want a reachable box let through", err)
	}
	if len(warnings) != 0 {
		t.Errorf("PreflightDeploy() warned %q about a box that answers on both ports", warnings)
	}
	slices.Sort(dialed)
	if want := []string{"box.invalid:443", "box.invalid:80"}; !slices.Equal(dialed, want) {
		t.Errorf("dialed %q, want %q: the box serves its hostnames on both", dialed, want)
	}
}

func TestABoxAnEdgeFrontsIsNotProbedFromOutside(t *testing.T) {
	t.Parallel()

	var dialed []string
	warnings, err := preflightOver(boxSaying(nil), cloudflare.Kind, reachingWith(map[string]error{
		"box.invalid:80": filtered, "box.invalid:443": filtered,
	}, &dialed))
	if err != nil {
		t.Fatalf("PreflightDeploy() = %v, want the deploy let through", err)
	}
	if len(dialed) != 0 || len(warnings) != 0 {
		t.Errorf("dialed %q and warned %q, want neither: an edge reaches the box through a tunnel or from its own addresses, which a firewall may rightly allow alone", dialed, warnings)
	}
}

func TestABoxWhoseProxyDoesNotOwnItsPortsIsRefusedWithoutAProbeFromOutside(t *testing.T) {
	t.Parallel()

	var dialed []string
	_, err := preflightOver(boxSaying(map[string]answer{
		"publish=443":         {stdout: "\n"},
		"/proc/net/tcp &&":    {stdout: socketTable(80)},
		"cat /proc/net/tcp\n": {stdout: socketTable(80)},
	}), edge.None, reachingWith(nil, &dialed))
	if err == nil || !strings.Contains(err.Error(), "443") {
		t.Fatalf("PreflightDeploy() = %v, want the box refused for nothing listening on 443", err)
	}
	if len(dialed) != 0 {
		t.Errorf("dialed %q, want nothing dialed: a port nothing on the box listens on is already refused with its own fix", dialed)
	}
}

func TestBothPortsAreProbedAtOnceUnderAShortDeadline(t *testing.T) {
	t.Parallel()

	var started sync.WaitGroup
	started.Add(2)
	both := make(chan struct{})
	go func() { started.Wait(); close(both) }()
	var mu sync.Mutex
	var deadlines []time.Duration
	reach := func(ctx context.Context, _ string) error {
		deadline, set := ctx.Deadline()
		mu.Lock()
		if set {
			deadlines = append(deadlines, time.Until(deadline))
		}
		mu.Unlock()
		started.Done()
		select {
		case <-both:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	warnings, err := preflightOver(boxSaying(nil), edge.None, reach)
	if err != nil {
		t.Fatalf("PreflightDeploy() = %v, want the deploy let through", err)
	}
	if len(warnings) != 0 {
		t.Errorf("PreflightDeploy() warned %q, want the two probes run side by side, each waiting for the other", warnings)
	}
	if len(deadlines) != 2 {
		t.Fatalf("probes ran with deadlines %v, want each probe bounded", deadlines)
	}
	for _, left := range deadlines {
		if left > 5*time.Second {
			t.Errorf("a probe may wait %v, want a few seconds at most: a filtered port answers nothing, and every deploy waits on it", left)
		}
	}
}

func TestABootstrapWhoseServingPortsAreClosedFromOutsideFails(t *testing.T) {
	t.Parallel()

	machine := boxSaying(nil)
	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (host.Conn, error) { return machine, nil },
	)
	var dialed []string
	p.Reaching(reachingWith(map[string]error{"box.invalid:80": filtered, "box.invalid:443": filtered}, &dialed))

	err := p.RefuseServingPortsClosedFromOutside(context.Background(), environment.TierProduction)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("RefuseServingPortsClosedFromOutside() = %v, want a not-ready refusal", err)
	}
	for _, wanted := range []string{"ports 80 and 443", "timed out", "security group", "ocel bootstrap production"} {
		if !strings.Contains(err.Error(), wanted) {
			t.Errorf("refusal = %q, want %q in it", err, wanted)
		}
	}
}

func TestABootstrapFailsAfterInstallingWhenItsPortsAreClosedFromOutside(t *testing.T) {
	t.Parallel()

	inner := &reached{}
	closed := func(context.Context, environment.Tier) error {
		return refusal.Refuse(refusal.CodeNotReady, "port %s is closed from outside", caddy.HTTPPort)
	}
	gated := vps.Elevating(inner, func(context.Context) error { return nil }, closed)

	if err := gated.Apply(context.Background(), provider.BootstrapRequest{Tier: environment.TierProduction}, nil); err == nil {
		t.Error("Apply() = nil, want the bootstrap failed: a box whose hostnames nothing outside reaches is not ready to serve")
	}
	if inner.applied != 1 {
		t.Errorf("the host was applied %d times, want once: the ports are only listened on once the proxy is installed", inner.applied)
	}
	if err := gated.Apply(context.Background(), provider.BootstrapRequest{Tier: environment.TierProduction, Repair: true}, nil); err != nil {
		t.Errorf("Apply(repair) = %v, want a deploy's repair let through: the deploy's own preflight warns about the ports", err)
	}
}

func reachedFromOutside(context.Context, string) error { return nil }
