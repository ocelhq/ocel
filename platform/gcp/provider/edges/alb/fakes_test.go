package alb

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/gcp/provider/pin"
)

const loadBalancerAddress = "34.117.0.7"

const notFoundBackend = "ocel-alb-production-notfound"

const backendToken = "gcp:compute/backendService:BackendService"

type world struct {
	mu       sync.Mutex
	outputs  map[string]map[string]string
	ups      []string
	destroys []string
	routed   map[string]map[string]string
	pinned   []string
	backends map[string]map[string]bool
	entries  map[string]map[string]bool
	declared map[string]map[string]declaration
	breaks   map[string]error
	pinning  error

	routeFails   map[string]error
	unrouteFails map[string]error

	invalidatedTags  [][]string
	invalidatedHosts []string
	invalidating     error
	untags           []string
	closed           map[string]bool
	onUp             func()
	through          []string
	warmFails        error
	untagFails       error

	backendServiceQuota *BackendServiceQuota
}

func (w *world) warmThrough(_ context.Context, url, address string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if address == "" {
		address = "the hostname's own address"
	}
	w.through = append(w.through, url+" via "+address)
	return w.warmFails
}

func (w *world) warmedThrough() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.through)
}

func newWorld() *world {
	return &world{
		outputs: map[string]map[string]string{
			LoadBalancerStack(environment.TierProduction): balancer(),
			LoadBalancerStack(environment.TierPreview):    balancer(),
		},
		routed:       map[string]map[string]string{},
		backends:     map[string]map[string]bool{"": {notFoundBackend: true}},
		entries:      map[string]map[string]bool{},
		declared:     map[string]map[string]declaration{},
		breaks:       map[string]error{},
		routeFails:   map[string]error{},
		unrouteFails: map[string]error{},
	}
}

func balancer() map[string]string {
	return map[string]string{
		"address":        loadBalancerAddress,
		"certificateMap": "ocel-alb-production-certs",
		"urlMap":         "ocel-alb-production-routes",
		"notFound":       notFoundBackend,
	}
}

func (w *world) Up(_ context.Context, target Target, program Program, _ progress.Log) (map[string]string, error) {
	stack := target.Name()
	if program == nil {
		return nil, errors.New("a stack was raised with no program to raise")
	}
	w.mu.Lock()
	overlapping := w.onUp
	w.onUp = nil
	w.mu.Unlock()
	seen, err := declared(program)
	if err != nil {
		return nil, err
	}
	if overlapping != nil {
		overlapping()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if broken := w.breaks[stack]; broken != nil {
		return nil, broken
	}
	w.ups = append(w.ups, stack)
	w.declared[stack] = seen
	backends := map[string]bool{}
	for name, declaration := range seen {
		if declaration.Token == backendToken {
			backends[name] = true
		}
	}
	w.backends[stack] = backends
	if trust, shielding := trustConfigOf(seen); shielding {
		if w.outputs[stack] == nil {
			w.outputs[stack] = map[string]string{}
		}
		w.outputs[stack][outputTrusted] = fingerprintTrusted(trust)
	}
	if outputs, up := w.outputs[stack]; up {
		return maps.Clone(outputs), nil
	}
	w.outputs[stack] = map[string]string{}
	return map[string]string{}, nil
}

func trustConfigOf(seen map[string]declaration) ([]string, bool) {
	for _, declared := range seen {
		if declared.Token != "gcp:certificatemanager/trustConfig:TrustConfig" {
			continue
		}
		anchors, allowlisted := readTrustConfig(declared)
		return slices.Concat(anchors, allowlisted), true
	}
	return nil, false
}

func (w *world) Destroy(_ context.Context, target Target, _ progress.Log) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.destroys = append(w.destroys, target.Name())
	delete(w.backends, target.Name())
	delete(w.declared, target.Name())
	return nil
}

func (w *world) Outputs(_ context.Context, target Target) (map[string]string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return maps.Clone(w.outputs[target.Name()]), nil
}

func (w *world) Route(_ context.Context, urlMap, hostname, backend string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.hasBackend(backend) {
		return fmt.Errorf("route %s onto the backend service %q, which no stack declared: Compute rejects a url map naming a backend that is not there",
			hostname, backend)
	}
	if err := w.routeFails[hostname]; err != nil {
		return err
	}
	hosts := w.routed[urlMap]
	if hosts == nil {
		hosts = map[string]string{}
		w.routed[urlMap] = hosts
	}
	if _, routed := hosts[hostname]; !routed && len(hosts) >= maxHostRules {
		return fmt.Errorf("route %s onto a url map already holding %d host rules, the most Compute allows", hostname, len(hosts))
	}
	hosts[hostname] = backend
	return nil
}

func (w *world) ServeNotFound(_ context.Context, urlMap, hostname string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	hosts := w.routed[urlMap]
	if hosts == nil {
		hosts = map[string]string{}
		w.routed[urlMap] = hosts
	}
	hosts[hostname] = notFoundBackend
	return nil
}

func (w *world) CountHostRules(_ context.Context, urlMap string) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.routed[urlMap]), nil
}

func (w *world) ReadBackendServiceQuota(context.Context) (BackendServiceQuota, bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.backendServiceQuota == nil {
		return BackendServiceQuota{}, false, nil
	}
	return *w.backendServiceQuota, true, nil
}

func (w *world) setBackendServiceQuota(quota BackendServiceQuota) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.backendServiceQuota = &quota
}

func (w *world) fillHostRules(urlMap string, count int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	hosts := w.routed[urlMap]
	if hosts == nil {
		hosts = map[string]string{}
		w.routed[urlMap] = hosts
	}
	for i := len(hosts); i < count; i++ {
		hosts[fmt.Sprintf("host-%d.example.com", i)] = notFoundBackend
	}
}

func (w *world) Unroute(_ context.Context, urlMap, hostname string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.unrouteFails[hostname]; err != nil {
		return err
	}
	delete(w.routed[urlMap], hostname)
	return nil
}

func (w *world) Entered(_ context.Context, certificateMap string) ([]string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Sorted(maps.Keys(w.entries[certificateMap])), nil
}

func (w *world) enter(certificateMap, hostname string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.entries[certificateMap] == nil {
		w.entries[certificateMap] = map[string]bool{}
	}
	w.entries[certificateMap][hostname] = true
}

func (w *world) Restore(ctx context.Context, service, revision string, stillActive router.StillActive) error {
	_, err := w.Pin(ctx, service, revision, stillActive)
	return err
}

func (w *world) Pin(ctx context.Context, service, revision string, stillActive router.StillActive) (bool, error) {
	for {
		w.mu.Lock()
		read, refused := len(w.pinned), w.pinning
		w.mu.Unlock()
		if refused != nil {
			return false, refused
		}
		if stillActive != nil {
			if err := stillActive(ctx); err != nil {
				return false, err
			}
		}
		w.mu.Lock()
		if len(w.pinned) != read {
			w.mu.Unlock()
			continue
		}
		w.pinned = append(w.pinned, service+"@"+revision)
		opened := w.closed[service]
		delete(w.closed, service)
		w.mu.Unlock()
		return opened, nil
	}
}

func (w *world) ReadServing(_ context.Context, service string) (string, error) {
	return w.pinnedRevision(service), nil
}

func (w *world) ReadTag(_ context.Context, service, revision string) (string, error) {
	return w.tagOf(service, revision), nil
}

func (w *world) ReadRollback(context.Context, string, string) (pin.Rollback, bool, error) {
	return pin.Rollback{}, false, nil
}

func (w *world) RecordRollback(context.Context, string, string, pin.Rollback) error { return nil }

func (w *world) Close(ctx context.Context, service string, stillActive router.StillActive) error {
	if stillActive != nil {
		if err := stillActive(ctx); err != nil {
			return err
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed == nil {
		w.closed = map[string]bool{}
	}
	w.closed[service] = true
	return nil
}

func (w *world) Warm(context.Context, string, string, string) error { return nil }

func (w *world) refuseUntags(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.untagFails = err
}

func (w *world) Untag(_ context.Context, service, tag string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.untagFails != nil {
		return w.untagFails
	}
	w.untags = append(w.untags, service+"#"+tag)
	return nil
}

func (w *world) untagged() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.untags)
}

func (w *world) tagOf(service, revision string) string {
	return "tag-" + strings.TrimPrefix(revision, service+"-")
}

func (w *world) servedRevision(slug string, tier environment.Tier, hostnames []string) string {
	declared := w.declarations(BindingStack(slug, tier))
	routed := w.hosts(balancer()["urlMap"])
	for _, hostname := range hostnames {
		backend, ok := routed[hostname]
		if !ok || backend == notFoundBackend {
			continue
		}
		run, _ := declared[negName(slug, tier, hostname)].Args["cloudRun"].(map[string]any)
		service, _ := run["service"].(string)
		tag, _ := run["tag"].(string)
		if tag == "" {
			return w.pinnedRevision(service)
		}
		return strings.TrimPrefix(tag, "tag-")
	}
	return ""
}

func (w *world) beforeNextUp(fn func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onUp = fn
}

func (w *world) refusePins(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pinning = err
}

func (w *world) pinnedRevision(service string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed[service] {
		return ""
	}
	for _, pin := range slices.Backward(w.pinned) {
		if pinned, revision, _ := strings.Cut(pin, "@"); pinned == service {
			return revision
		}
	}
	return ""
}

func (w *world) hasBackend(backend string) bool {
	for _, stackBackends := range w.backends {
		if stackBackends[backend] {
			return true
		}
	}
	return false
}

func (w *world) raised() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.ups)
}

func (w *world) torn() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.destroys)
}

func (w *world) hosts(urlMap string) map[string]string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return maps.Clone(w.routed[urlMap])
}

func (w *world) breakUp(stack string, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.breaks[stack] = err
}

func (w *world) declarations(stack string) map[string]declaration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return maps.Clone(w.declared[stack])
}

func (w *world) pins() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.pinned)
}

var _ Stacks = (*world)(nil)

var _ Routes = (*world)(nil)

var _ Entries = (*world)(nil)

func declared(program Program) (map[string]declaration, error) {
	seen := map[string]declaration{}
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		return program(ctx, "acme-prod")
	}, pulumi.WithMocks("alb", "test", mocks{mu: &sync.Mutex{}, seen: seen}))
	return seen, err
}

type declaration struct {
	Token string
	Args  map[string]any
}

type mocks struct {
	mu   *sync.Mutex
	seen map[string]declaration
}

func (m mocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen[args.Name] = declaration{Token: args.TypeToken, Args: args.Inputs.Mappable()}
	outputs := args.Inputs.Copy()
	outputs["selfLink"] = resource.NewStringProperty("https://compute.example.com/" + args.Name)
	if args.TypeToken == "gcp:compute/globalAddress:GlobalAddress" {
		outputs["address"] = resource.NewStringProperty(loadBalancerAddress)
	}
	return args.Name + "-id", outputs, nil
}

func (mocks) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return args.Args, nil
}

func (w *world) refuseRoute(hostname string, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.routeFails[hostname] = err
}

func (w *world) refuseUnroute(hostname string, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.unrouteFails[hostname] = err
}
