package host

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/release"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
	"github.com/ocelhq/ocel/platform/vps/provider/tunnel"
)

type bench struct {
	mu        sync.Mutex
	dest      ssh.Destination
	facts     ssh.Facts
	installed map[environment.Tier][]machine.Item
	ran       []string
	fed       []string
	dials     int
	paused    []time.Duration
	dead      error
	floor     error
	answer    func(command string) (ssh.Result, bool)
	broke     func(command string) error
	after     func(b *bench, command string)
	followed  func(command string, each func(ssh.Line) error) error
}

func newBench(installed map[environment.Tier][]machine.Item) *bench {
	return &bench{
		dest: ssh.Destination{
			Written:    "ocelbox",
			Address:    "203.0.113.10",
			Port:       2222,
			User:       deployUser,
			KnownHosts: []string{"/home/ada/.ssh/known_hosts"},
		},
		facts:     ssh.Facts{Root: true, Systemd: true, Arch: "x86_64"},
		installed: installed,
	}
}

func (b *bench) host() *Host { return b.fronted(front.Proxy{}) }

func (b *bench) fronted(fronting front.Proxy) *Host {
	return New(ssh.NewBox(b.dial, b.pause), "shop", Keys{}, nil, fronting)
}

func (h *Host) useFront(front proxy.Proxy) {
	h.front = front
	h.routing = routing.NewTable(h.Box, front, "shop", h.pins.Verified)
	h.tunnel = tunnel.New(h.Box, h.routing)
	h.containers = release.NewContainers(h.Box, h.engine, h.routing, front, h.images)
}

func (b *bench) pause(ctx context.Context, wait time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.paused = append(b.paused, wait)
	return ctx.Err()
}

func (b *bench) waits() []time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]time.Duration(nil), b.paused...)
}

type recorder struct{ said *[]string }

func saying(said *[]string) progress.Log { return recorder{said: said} }

func (r recorder) Say(message string) { *r.said = append(*r.said, message) }

func (r recorder) Warn(message string) { r.Say(message) }

func (r recorder) Error(message string) { r.Say(message) }

func (recorder) Detail(string) {}

func (recorder) Debug(string) {}

func (recorder) Span(string, time.Time, time.Time, error, ...progress.Attr) {}

func (b *bench) dial(ctx context.Context) (ssh.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dials++
	if b.dead != nil {
		return nil, b.dead
	}
	return wire{b}, nil
}

func drained(stdin io.Reader) (string, error) {
	if stdin == nil {
		return "", nil
	}
	raw, err := io.ReadAll(stdin)
	return string(raw), err
}

func (b *bench) commands() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.ran...)
}

func (b *bench) taking() []string {
	var taken []string
	for _, command := range b.commands() {
		if strings.HasPrefix(command, "rm ") || strings.HasPrefix(command, "rm -rf ") ||
			strings.HasPrefix(command, "rmdir ") || strings.HasPrefix(command, "userdel ") ||
			strings.HasPrefix(command, "docker ") || strings.HasPrefix(command, "if ! docker ") ||
			strings.HasPrefix(command, routing.Locked("-x")+"rm ") {
			taken = append(taken, command)
		}
	}
	return taken
}

func (b *bench) at(fragment string) int {
	for at, command := range b.commands() {
		if strings.Contains(command, fragment) {
			return at
		}
	}
	return -1
}

func (b *bench) took(fragment string) int {
	for at, command := range b.taking() {
		if strings.Contains(command, fragment) {
			return at
		}
	}
	return -1
}

type wire struct{ b *bench }

func (w wire) Destination() ssh.Destination { return w.b.dest }

func (w wire) Preflight(context.Context) (ssh.Facts, error) { return w.b.facts, w.b.floor }

func (w wire) ForwardPort(context.Context, string) (string, func(), error) {
	return "", nil, errors.New("the bench forwards no port")
}

func (w wire) ForwardSpans() string { return "" }

func (w wire) Run(ctx context.Context, command string) (string, error) {
	result, err := w.Stream(ctx, command, nil)
	if err != nil {
		return "", err
	}
	if result.Code != 0 {
		return "", errors.New(result.Stderr)
	}
	return result.Stdout, nil
}

func (w wire) Stream(_ context.Context, command string, stdin io.Reader) (ssh.Result, error) {
	b := w.b
	fed, err := drained(stdin)
	if err != nil {
		return ssh.Result{}, err
	}
	b.mu.Lock()
	b.ran = append(b.ran, command)
	b.fed = append(b.fed, fed)
	answer, broke, hook := b.answer, b.broke, b.after
	b.mu.Unlock()

	if broke != nil {
		if err := broke(command); err != nil {
			return ssh.Result{}, err
		}
	}
	result := b.rendered(command)
	if answer != nil {
		if scripted, said := answer(command); said {
			result = scripted
		}
	}
	if hook != nil {
		hook(b, command)
	}
	return result, nil
}

func (w wire) RunLines(_ context.Context, command string, each func(ssh.Line) error) error {
	b := w.b
	b.mu.Lock()
	b.ran = append(b.ran, command)
	b.fed = append(b.fed, "")
	followed := b.followed
	b.mu.Unlock()
	if followed == nil {
		return nil
	}
	return followed(command, each)
}

func (b *bench) rendered(command string) ssh.Result {
	switch {
	case strings.Contains(command, "id -u"):
		return ssh.Result{Stdout: "0\n"}
	case command == "uname -m":
		return ssh.Result{Stdout: b.facts.Arch + "\n"}
	case strings.Contains(command, docker.SizeFields):
		return ssh.Result{Stdout: strconv.Itoa(benchMemoryBytes) + " " + strconv.Itoa(benchCPUs) + "\n"}
	case readsProxy(command):
		return ssh.Result{Stdout: pairSaid(string(routingTableItem().Content), string(proxyConfigItem().Content))}
	case strings.Contains(command, "for p in"):
		for tier, items := range b.installed {
			if strings.Contains(command, shell.Quote(machine.StampPath(tier))) {
				return ssh.Result{Stdout: surveyed(items)}
			}
		}
		return ssh.Result{}
	case command == frontReading():
		for _, items := range b.installed {
			for _, item := range items {
				if item.Name == FrontRecordPath {
					return ssh.Result{Stdout: string(item.Content)}
				}
			}
		}
		return ssh.Result{}
	case strings.Contains(command, "'--filter' 'publish="):
		for _, items := range b.installed {
			if slices.ContainsFunc(items, func(item machine.Item) bool {
				return item.Kind == machine.KindContainer && item.Name == caddy.Container
			}) {
				return ssh.Result{Stdout: caddy.Container + "\n"}
			}
		}
		return ssh.Result{}
	case strings.HasPrefix(command, ownersCommand):
		return ssh.Result{}
	case readsEnv(command):
		return ssh.Result{Stdout: "null\n"}
	case strings.Contains(command, shell.Quote(release.ReleaseSelectors())):
		return ssh.Result{Stdout: "shop web " + appImage + "\n"}
	case strings.HasPrefix(command, "cat ") && !strings.HasPrefix(command, "cat | "):
		for _, items := range b.installed {
			for _, item := range items {
				if command == "cat "+shell.Quote(item.Name) {
					return ssh.Result{Stdout: string(item.Content)}
				}
			}
		}
		return ssh.Result{Code: 1, Stderr: "no such file or directory"}
	default:
		return ssh.Result{}
	}
}

func surveyed(items []machine.Item) string {
	var rendered strings.Builder
	for _, item := range items {
		fmt.Fprintf(&rendered, "%s\t%s\t%o\t%s\t%s", item.Kind, item.Name, item.Mode, item.Owner, item.Sum())
		if item.Kind == machine.KindSealKey {
			rendered.WriteString("\t2026-01-01T00:00:00Z")
		}
		rendered.WriteString("\n")
		if item.ID() == engineItem().ID() {
			fmt.Fprintf(&rendered, "%s\t%s\t0\t%s\t%s\n", machine.EngineFactsRow, dockerEngine, engineStandard, "28.3.1")
		}
	}
	return rendered.String()
}

func bootstrapped(t *testing.T, tier environment.Tier) []machine.Item {
	t.Helper()
	stamp, err := machine.Stamp{State: machine.StateComplete}.Item(tier)
	if err != nil {
		t.Fatal(err)
	}
	return append(Items(tier, []byte(aKey+"\n"), ssh.AMD64, front.Proxy{}), stamp)
}

func TestASurveyedHostReadsBackAsTheItemsThatAreInstalledOnIt(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	items := Items(tier, []byte(aKey+"\n"), ssh.AMD64, front.Proxy{})
	box := newBench(map[environment.Tier][]machine.Item{tier: items})

	read, err := box.host().Survey(context.Background(), tier)
	if err != nil {
		t.Fatalf("Survey() = %v", err)
	}
	for _, item := range items {
		if !read.current(item) {
			t.Errorf("%s surveys as absent or moved, so nothing this bench proves about a bootstrapped host means anything", item.ID())
		}
	}
}

func bootstrappedOn(t *testing.T, tier environment.Tier) *bench {
	t.Helper()
	seeded := string(routingTableItem().Content)
	return bootstrappedWith(t, tier, &seeded, nil)
}

func bootstrappedWith(t *testing.T, tier environment.Tier, table, config *string) *bench {
	t.Helper()
	return bootstrappedBehind(t, tier, front.Proxy{}, table, config)
}

func bootstrappedBehind(t *testing.T, tier environment.Tier, fronting front.Proxy, table, config *string) *bench {
	t.Helper()

	keys := []byte(aKey + "\n")
	items := Items(tier, keys, ssh.AMD64, fronting)
	minted := []byte("the key this box minted for itself")
	present := make([]machine.Item, 0, len(items)+1)
	for _, item := range items {
		if item.Kind == machine.KindSealKey {
			item.Content = minted
		}
		present = append(present, item)
	}
	record, err := frontRecordItem(fronting, "shop", tier)
	if err != nil {
		t.Fatal(err)
	}
	stamp, err := machine.Stamp{
		State:   machine.StateComplete,
		Writer:  "the-suite",
		Seal:    machine.Seal{Fingerprint: machine.ContentSum(minted), Algorithm: machine.SealAlgorithm, CreatedAt: "2026-01-01T00:00:00Z"},
		Digests: machine.DigestsOf(append(items, record)),
	}.Item(tier)
	if err != nil {
		t.Fatal(err)
	}
	box := newBench(map[environment.Tier][]machine.Item{tier: append(present, stamp, record)})
	proxied := servesPair(box, table, config)
	box.answer = func(command string) (ssh.Result, bool) {
		if command == "cat ~/.ssh/authorized_keys 2>/dev/null" {
			return ssh.Result{Stdout: aKey + "\n"}, true
		}
		return proxied(command)
	}
	return box
}

func TestABoxAtTheStampAWriteLeftDescribesItselfAsCurrent(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	box := bootstrappedOn(t, tier)

	described, err := NewBootstrap(box.host(), testVendor, "shop").Describe(context.Background(), tier)
	if err != nil {
		t.Fatalf("Describe() = %v", err)
	}
	if !described.Present {
		t.Fatal("Describe() reads no bootstrap where a stamp exists")
	}
	if !described.Stacks[0].DigestCurrent {
		t.Fatalf("Describe() calls a box at every digest its stamp records drifted, and a box that can never report itself up to date is one every re-run rewrites: %+v",
			described.Stacks)
	}
}

func TestOneProbeThatCouldNotLookRefusesTheWholeReadingRatherThanPlanningOverIt(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	box := bootstrappedOn(t, tier)
	answered := box.answer
	box.answer = func(command string) (ssh.Result, bool) {
		if strings.Contains(command, "for p in") {
			return ssh.Result{Stdout: strings.Join([]string{machine.KindUnreadable, deployUser, "0", machine.KindUser, "getent passwd exited 126"}, "\t") + "\n"}, true
		}
		return answered(command)
	}

	described, err := NewBootstrap(box.host(), testVendor, "shop").Describe(context.Background(), tier)
	if err == nil {
		t.Fatalf("Describe() over a survey that could not run one of its probes = %+v, want a refusal: a plan built on it writes over whatever the probe could not see", described)
	}
	if !strings.Contains(err.Error(), deployUser) {
		t.Errorf("Describe() refused with %q and never names what could not be read", err)
	}
}

func TestOneItemTheReadingCannotHashIsDriftRatherThanAnAbsence(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	box := bootstrappedOn(t, tier)
	installed := box.installed[tier]
	for at, item := range installed {
		if item.Name == boxapi.SwitchboardBinary {
			installed[at].Content = nil
		}
	}

	read, err := box.host().Read(context.Background(), tier)
	if err != nil {
		t.Fatalf("Read() = %v", err)
	}
	if read.current(proxyItem(machine.KindFile)) {
		t.Fatal("the reading calls a helper it read no bytes of current, so this test proves nothing about what drift is")
	}
	if !read.observed(machine.KindFile, boxapi.SwitchboardBinary) {
		t.Errorf("%s is absent from a reading every write is planned against, and absence is a lie there: a file this reading could not hash is drift it must name",
			boxapi.SwitchboardBinary)
	}
	if read.upToDate() {
		t.Errorf("a box whose %s does not hash as ocel writes it reports itself up to date", boxapi.SwitchboardBinary)
	}
}

func digested(document string) string {
	sum := sha256.Sum256([]byte(document))
	return hex.EncodeToString(sum[:])
}

func readsProxy(command string) bool {
	return strings.Contains(command, "flock -s 9")
}

func writesProxy(command string) bool {
	return strings.Contains(command, `IFS= read -r written`)
}

func expectedDigest(command string) string {
	_, checked, _ := strings.Cut(command, `if [ "$current" != `)
	expected, _, _ := strings.Cut(checked, " ]")
	return strings.Trim(expected, "'")
}

func fedLine(fed string, at int) string {
	lines := strings.Split(fed, "\n")
	if len(lines) <= at {
		return ""
	}
	decoded, err := base64.StdEncoding.DecodeString(lines[at])
	if err != nil {
		return ""
	}
	return string(decoded)
}

func tableOf(fed string) string { return fedLine(fed, 0) }

func configOf(fed string) string { return fedLine(fed, 1) }

func renderedFrom(document string) string {
	table, err := routing.ReadRoutingTable([]byte(document))
	if err != nil {
		return err.Error()
	}
	rendered, err := routing.RenderProxyConfig(caddy.Builtin{}, table)
	if err != nil {
		return err.Error()
	}
	return string(rendered)
}

func pairSaid(table, config string) string {
	line := func(value string) string {
		if value == "" {
			return "\n"
		}
		return "+" + base64.StdEncoding.EncodeToString([]byte(value)) + "\n"
	}
	return line(table) + line(config)
}

func servesProxy(b *bench, table *string) func(string) (ssh.Result, bool) {
	return servesPair(b, table, nil)
}

func servesPair(b *bench, table, config *string) func(string) (ssh.Result, bool) {
	return func(command string) (ssh.Result, bool) {
		b.mu.Lock()
		defer b.mu.Unlock()
		switch {
		case writesProxy(command):
			if expected := expectedDigest(command); expected != digested(*table) {
				return ssh.Result{Code: routing.ExitMoved, Stderr: digested(*table)}, true
			}
			*table = tableOf(b.fed[len(b.fed)-1])
			if config != nil {
				*config = configOf(b.fed[len(b.fed)-1])
			}
			return ssh.Result{Stdout: digested(*table)}, true
		case readsProxy(command):
			if config != nil {
				return ssh.Result{Stdout: pairSaid(*table, *config)}, true
			}
			return ssh.Result{Stdout: pairSaid(*table, renderedFrom(*table))}, true
		default:
			return ssh.Result{}, false
		}
	}
}

func admittedBy(t *testing.T, state boxapi.RoutingTable) func(hostname string) bool {
	t.Helper()
	table, err := boxapi.ReadTable(mustWrite(t, state))
	if err != nil {
		t.Fatalf("the switchboard refuses the table this state writes: %v", err)
	}
	return table.Admits
}

func routedBy(t *testing.T, state boxapi.RoutingTable) func(hostname, path string) (string, bool) {
	t.Helper()
	table, err := boxapi.ReadTable(mustWrite(t, state))
	if err != nil {
		t.Fatalf("the switchboard refuses the table this state writes: %v", err)
	}
	return func(hostname, path string) (string, bool) {
		forward, ok := table.Forward(hostname, path)
		return forward.Upstream, ok
	}
}
