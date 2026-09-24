package host

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/constants"
	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/caddyadmin"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

const (
	proxyServer      = "ocel"
	proxyDrainServer = "ocel_drain"
	drainListen      = "127.0.0.1:9"
	routeIdentity    = "ocel-app-"
	claimSeparator   = live.ClaimSeparator
	drainIdentity    = "ocel-retiring"
	boxIdentity      = "ocel-box"
	connectorRoute   = "ocel-connector"
)

const ConnectorPath = "/" + constants.ProjectStateDirName + "/connector"

const ConnectorDial = "unix/" + ConnectorSocket

func previewEntryIdentity(base string) string {
	return edge.PreviewEntryOwner + claimSeparator + base
}

func previewProbeIdentity(base string) string {
	return edge.LivenessProbeLabel + claimSeparator + base
}

const (
	EdgeHeader = "X-Ocel-Edge"
	EdgeName   = "box"
)

const (
	edgeHandler    = "headers"
	forwardHandler = "reverse_proxy"
	refuseHandler  = "static_response"
	rewriteHandler = "rewrite"
)

const (
	DeployWindow = 60 * time.Second
	DrainWindow  = 30 * time.Second
)

type RouteKey struct {
	Owner   string
	Pointer string
	App     string
}

func (k RouteKey) identity() string {
	return routeIdentity + strings.Join([]string{k.Owner, k.Pointer, k.App}, claimSeparator)
}

type AppRoute struct {
	RouteKey
	Upstream string
	Health   string
}

const (
	healthInterval = 10 * time.Second
	healthTimeout  = 5 * time.Second
	healthExpects  = 2
)

type HostClaim struct {
	Hostname string
	Owner    string
	Pointer  string
	App      string
}

type claimKey struct {
	Owner   string
	Pointer string
	App     string
}

type surfaceKey struct {
	Owner   string
	Pointer string
}

func (c HostClaim) key() claimKey {
	return claimKey{Owner: c.Owner, Pointer: c.Pointer, App: c.App}
}

func (r AppRoute) surface() surfaceKey {
	return surfaceKey{Owner: r.Owner, Pointer: r.Pointer}
}

func (c HostClaim) identity() string {
	return live.ClaimIdentity(live.Claimed{Owner: c.Owner, Hostname: c.Hostname, Pointer: c.Pointer, App: c.App})
}

func (r AppRoute) app() bool { return r.App != live.StoreLabel }

type Pin struct {
	Hostname string
	Path     string
}

const (
	pinCertificate = ".crt"
	pinKey         = ".key"
)

func PinCertificate(path string) string { return path + pinCertificate }

func PinKey(path string) string { return path + pinKey }

func (p Pin) Covers(hostname string) bool {
	return (certs.Leaf{Domains: []string{p.Hostname}}).Covers(hostname)
}

func Covering(pins []Pin, hostname string) string {
	for _, exact := range []bool{true, false} {
		at := slices.IndexFunc(pins, func(pin Pin) bool {
			return exact == (pin.Hostname == hostname) && pin.Covers(hostname) && validPin(pin) == nil
		})
		if at >= 0 {
			return pins[at].Path
		}
	}
	return ""
}

type ProxyState struct {
	Grace       time.Duration
	Claims      []HostClaim
	Routes      []AppRoute
	Pins        []Pin
	PreviewBase string
	Retiring    []string
	Connector   string
}

const (
	dnsLabelMax = 63
	dnsNameMax  = 253
)

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

func PreviewBaseUsable(base string) error {
	if base == "" {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"the preview entry on this box has no base domain")
	}
	named := strings.ToLower(base)
	labels := strings.Split(named, ".")
	usable := len(labels) > 1 && len(named)+len(edge.LivenessProbeLabel)+1 <= dnsNameMax
	for _, label := range labels {
		usable = usable && len(label) <= dnsLabelMax && dnsLabel.MatchString(label)
	}
	if !usable {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"%q is not a usable preview base: want dotted dns labels of ≤%d bytes, with %s ≤%d bytes",
			base, dnsLabelMax, edge.ProbeHostname(edge.PreviewWildcard(base)), dnsNameMax)
	}
	return nil
}

func Claiming(claims []HostClaim, taken HostClaim) ([]HostClaim, error) {
	if at := slices.IndexFunc(claims, func(claim HostClaim) bool { return claim.Hostname == taken.Hostname }); at >= 0 && claims[at].Owner != taken.Owner {
		return nil, providerkit.Refuse(providerkit.CodeBusy,
			"%s is claimed on this box by %s\nUnbind it there before %s binds it",
			taken.Hostname, claims[at].Owner, taken.Owner)
	}
	kept := slices.DeleteFunc(slices.Clone(claims), func(claim HostClaim) bool { return claim.Hostname == taken.Hostname })
	return append(kept, taken), nil
}

func Disclaiming(claims []HostClaim, dropped func(HostClaim) bool) []HostClaim {
	return slices.DeleteFunc(slices.Clone(claims), dropped)
}

func Routing(routes []AppRoute, taken AppRoute) []AppRoute {
	return append(Unrouting(routes, func(route AppRoute) bool { return route.RouteKey == taken.RouteKey }), taken)
}

func Unrouting(routes []AppRoute, dropped func(AppRoute) bool) []AppRoute {
	return slices.DeleteFunc(slices.Clone(routes), dropped)
}

type caddyConfig struct {
	Admin   caddyAdmin      `json:"admin"`
	Logging json.RawMessage `json:"logging,omitempty"`
	Apps    caddyApps       `json:"apps"`
}

type caddyAdmin struct {
	Listen string `json:"listen"`
}

type caddyApps struct {
	HTTP caddyHTTP       `json:"http"`
	TLS  *caddyTLS       `json:"tls,omitempty"`
	PKI  json.RawMessage `json:"pki,omitempty"`
}

type caddyTLS struct {
	Certificates caddyCertificates `json:"certificates"`
	Automation   json.RawMessage   `json:"automation,omitempty"`
}

type caddyCertificates struct {
	LoadFiles []caddyLoadFile `json:"load_files"`
}

type caddyLoadFile struct {
	Certificate string   `json:"certificate"`
	Key         string   `json:"key"`
	Tags        []string `json:"tags"`
}

type caddyHTTP struct {
	GracePeriod string                 `json:"grace_period"`
	Servers     map[string]caddyServer `json:"servers"`
}

type caddyServer struct {
	Listen    []string        `json:"listen"`
	Logs      json.RawMessage `json:"logs,omitempty"`
	Automatic *caddyAutomatic `json:"automatic_https,omitempty"`
	Routes    []caddyRoute    `json:"routes"`
}

type caddyAutomatic struct {
	SkipCertificates []string `json:"skip_certificates"`
}

type caddyRoute struct {
	Identity string         `json:"@id,omitempty"`
	Match    []caddyMatch   `json:"match,omitempty"`
	Handle   []caddyForward `json:"handle"`
}

type caddyMatch struct {
	Host *[]string `json:"host,omitempty"`
	Path []string  `json:"path,omitempty"`
}

func (m caddyMatch) hosts() []string {
	if m.Host == nil {
		return nil
	}
	return *m.Host
}

func hostnamesOf(hostnames ...string) *[]string {
	named := append([]string{}, hostnames...)
	return &named
}

type caddyForward struct {
	Handler         string              `json:"handler"`
	Upstreams       []caddyDial         `json:"upstreams,omitempty"`
	HealthChecks    *caddyHealthChecks  `json:"health_checks,omitempty"`
	Status          int                 `json:"status_code,omitempty"`
	Headers         map[string][]string `json:"headers,omitempty"`
	Response        *caddyHeaderOps     `json:"response,omitempty"`
	StripPathPrefix string              `json:"strip_path_prefix,omitempty"`
}

type caddyHeaderOps struct {
	Set map[string][]string `json:"set"`
}

type caddyDial struct {
	Dial string `json:"dial"`
}

type caddyHealthChecks struct {
	Active caddyActiveHealth `json:"active"`
}

type caddyActiveHealth struct {
	URI          string `json:"uri"`
	Interval     string `json:"interval"`
	Timeout      string `json:"timeout"`
	ExpectStatus int    `json:"expect_status"`
}

func checking(health string) *caddyHealthChecks {
	if health == "" {
		return nil
	}
	return &caddyHealthChecks{Active: caddyActiveHealth{
		URI: health, Interval: spelled(healthInterval), Timeout: spelled(healthTimeout), ExpectStatus: healthExpects,
	}}
}

func namingTheEdge() caddyForward {
	return caddyForward{
		Handler:  edgeHandler,
		Response: &caddyHeaderOps{Set: map[string][]string{EdgeHeader: {EdgeName}}},
	}
}

func forwarding(identity string, upstreams ...string) caddyRoute {
	dials := make([]caddyDial, 0, len(upstreams))
	for _, upstream := range upstreams {
		dials = append(dials, caddyDial{Dial: upstream})
	}
	return caddyRoute{
		Identity: identity,
		Handle: []caddyForward{namingTheEdge(), {
			Handler:   forwardHandler,
			Upstreams: dials,
		}},
	}
}

func refusing(identity string) caddyRoute {
	return caddyRoute{
		Identity: identity,
		Handle: []caddyForward{{
			Handler: refuseHandler,
			Status:  http.StatusNotFound,
			Headers: map[string][]string{EdgeHeader: {EdgeName}},
		}},
	}
}

func refusingHost(identity, hostname string) caddyRoute {
	route := refusing(identity)
	route.Match = []caddyMatch{{Host: hostnamesOf(hostname)}}
	return route
}

const storeAdminSuffix = claimSeparator + "admin"

var storeAdminPaths = []string{"/rustfs", "/rustfs/*", "/health", "/health/*"}

func refusesStoreAdmin(route caddyRoute) bool {
	if len(route.Match) != 1 || !slices.Equal(route.Match[0].Path, storeAdminPaths) {
		return false
	}
	return len(route.Handle) == 1 &&
		route.Handle[0].Handler == refuseHandler &&
		route.Handle[0].Status == http.StatusNotFound
}

func refusingStoreAdmin(route AppRoute, hostnames []string) caddyRoute {
	refused := refusing(route.identity() + storeAdminSuffix)
	refused.Match = []caddyMatch{{Host: hostnamesOf(hostnames...), Path: storeAdminPaths}}
	return refused
}

func previewRoutes(base string) ([]caddyRoute, error) {
	if base == "" {
		return nil, nil
	}
	if err := PreviewBaseUsable(base); err != nil {
		return nil, err
	}
	wildcard := edge.PreviewWildcard(base)
	return []caddyRoute{
		refusingHost(previewProbeIdentity(base), edge.ProbeHostname(wildcard)),
		refusingHost(previewEntryIdentity(base), wildcard),
	}, nil
}

func connectorForwarding(hostname string) caddyRoute {
	return caddyRoute{
		Identity: connectorRoute,
		Match:    []caddyMatch{{Host: hostnamesOf(hostname), Path: []string{ConnectorPath, ConnectorPath + "/*"}}},
		Handle: []caddyForward{
			namingTheEdge(),
			{Handler: rewriteHandler, StripPathPrefix: ConnectorPath},
			{Handler: forwardHandler, Upstreams: []caddyDial{{Dial: ConnectorDial}}},
		},
	}
}

func connectorHostOf(route caddyRoute) string {
	if len(route.Match) != 1 || len(route.Match[0].hosts()) != 1 {
		return ""
	}
	return route.Match[0].hosts()[0]
}

func matching(app AppRoute, hostnames []string) caddyRoute {
	route := forwarding(app.identity(), app.Upstream)
	route.Handle[1].HealthChecks = checking(app.Health)
	route.Match = []caddyMatch{{Host: hostnamesOf(hostnames...)}}
	return route
}

func RenderProxyConfig(state ProxyState) ([]byte, error) {
	seeded, err := seededConfig()
	if err != nil {
		return nil, err
	}
	routes := make([]caddyRoute, 0, len(state.Claims)+len(state.Routes)+2)
	if state.Connector != "" {
		routes = append(routes, connectorForwarding(state.Connector))
	}
	claimed := map[claimKey][]string{}
	for _, claim := range slices.SortedFunc(slices.Values(state.Claims), func(a, b HostClaim) int {
		return strings.Compare(a.Hostname, b.Hostname)
	}) {
		if err := validClaim(claim); err != nil {
			return nil, err
		}
		routes = append(routes, caddyRoute{
			Identity: claim.identity(),
			Match:    []caddyMatch{{Host: hostnamesOf(claim.Hostname)}},
			Handle:   []caddyForward{},
		})
		claimed[claim.key()] = append(claimed[claim.key()], claim.Hostname)
	}
	standing := slices.SortedFunc(slices.Values(state.Routes), byKey)
	running := map[surfaceKey][]string{}
	for _, route := range standing {
		if err := validRoute(route); err != nil {
			return nil, err
		}
		if !route.app() {
			continue
		}
		if !slices.Contains(running[route.surface()], route.App) {
			running[route.surface()] = append(running[route.surface()], route.App)
		}
	}
	for _, under := range slices.SortedFunc(maps.Keys(running), bySurface) {
		wide := claimed[claimKey{Owner: under.Owner, Pointer: under.Pointer}]
		if len(wide) == 0 || len(running[under]) == 1 {
			continue
		}
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"%s claims %s under %s, which runs %s\nDeclare domains.production on the app that serves it, not the project",
			under.Owner, strings.Join(wide, ", "), under.Pointer, strings.Join(running[under], " and "))
	}
	answering := map[string]AppRoute{}
	for _, route := range standing {
		hostnames := claimed[claimKey{Owner: route.Owner, Pointer: route.Pointer, App: route.App}]
		if route.app() && len(running[route.surface()]) == 1 {
			hostnames = append(slices.Clone(hostnames), claimed[claimKey{Owner: route.Owner, Pointer: route.Pointer}]...)
		}
		slices.Sort(hostnames)
		for _, hostname := range hostnames {
			if held, taken := answering[hostname]; taken {
				return nil, providerkit.Refuse(providerkit.CodeInvalid,
					"%s would be forwarded by both %s and %s",
					hostname, held.identity(), route.identity())
			}
			answering[hostname] = route
		}
		if !route.app() && len(hostnames) > 0 {
			routes = append(routes, refusingStoreAdmin(route, hostnames))
		}
		routes = append(routes, matching(route, hostnames))
	}
	entry, err := previewRoutes(state.PreviewBase)
	if err != nil {
		return nil, err
	}
	routes = append(routes, entry...)
	routes = append(routes, refusing(boxIdentity))

	live := seeded.Apps.HTTP.Servers[proxyServer]
	live.Routes = routes
	live.Automatic = skipping(state.PreviewBase)
	pinned, err := loadFiles(state.Pins)
	if err != nil {
		return nil, err
	}
	rendered := caddyConfig{
		Admin:   caddyAdmin{Listen: caddyadmin.Listen(ProxyAdminSocket)},
		Logging: seeded.Logging,
		Apps: caddyApps{
			HTTP: caddyHTTP{
				GracePeriod: spelled(state.Grace),
				Servers:     map[string]caddyServer{proxyServer: live},
			},
			TLS: pinned,
			PKI: seeded.Apps.PKI,
		},
	}
	if len(state.Retiring) > 0 {
		retiring := slices.Sorted(slices.Values(state.Retiring))
		rendered.Apps.HTTP.Servers[proxyDrainServer] = caddyServer{
			Listen: []string{drainListen},
			Routes: []caddyRoute{forwarding(drainIdentity, slices.Compact(retiring)...)},
		}
	}
	return json.Marshal(rendered)
}

func skipping(base string) *caddyAutomatic {
	if base == "" {
		return nil
	}
	return &caddyAutomatic{SkipCertificates: []string{edge.PreviewWildcard(base)}}
}

func loadFiles(pins []Pin) (*caddyTLS, error) {
	if len(pins) == 0 {
		return nil, nil
	}
	files := make([]caddyLoadFile, 0, len(pins))
	at := map[string]int{}
	for _, pin := range slices.SortedFunc(slices.Values(pins), func(a, b Pin) int {
		return strings.Compare(a.Hostname, b.Hostname)
	}) {
		if err := validPin(pin); err != nil {
			return nil, err
		}
		if held, loaded := at[pin.Path]; loaded {
			if !slices.Contains(files[held].Tags, pin.Hostname) {
				files[held].Tags = append(files[held].Tags, pin.Hostname)
			}
			continue
		}
		mounted := pinMount(pin.Path)
		at[pin.Path] = len(files)
		files = append(files, caddyLoadFile{
			Certificate: PinCertificate(mounted),
			Key:         PinKey(mounted),
			Tags:        []string{pin.Hostname},
		})
	}
	return &caddyTLS{Certificates: caddyCertificates{LoadFiles: files}}, nil
}

func pinMount(path string) string { return proxyPinsMount + strings.TrimPrefix(path, ProxyPins) }

func pinnedAt(mounted string) (string, bool) {
	under, ok := strings.CutPrefix(mounted, proxyPinsMount+"/")
	if !ok || !pinLeaf(under) {
		return "", false
	}
	return ProxyPins + "/" + under, true
}

func pinLeaf(under string) bool {
	return under != "" && !strings.Contains(under, "/") && under != "." && under != ".."
}

func pinnedUnderProxyPins(path string) bool {
	under, beneath := strings.CutPrefix(path, ProxyPins+"/")
	return beneath && pinLeaf(under)
}

func validPin(pin Pin) error {
	if pin.Hostname == "" || !pinnedUnderProxyPins(pin.Path) {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"pinned certificate for %q is at %q, not under %s/",
			pin.Hostname, pin.Path, ProxyPins)
	}
	return nil
}

func previewRouteOf(route caddyRoute) (base, named string) {
	for _, label := range []string{edge.PreviewEntryOwner, edge.LivenessProbeLabel} {
		if rest, mine := strings.CutPrefix(route.Identity, label+claimSeparator); mine {
			return rest, label
		}
	}
	return "", ""
}

func previewRouteRead(route caddyRoute, base, named string) error {
	if err := PreviewBaseUsable(base); err != nil {
		return err
	}
	wildcard := edge.PreviewWildcard(base)
	hostname := wildcard
	if named == edge.LivenessProbeLabel {
		hostname = edge.ProbeHostname(wildcard)
	}
	if want := refusingHost(route.Identity, hostname); !routeEqual(route, want) {
		return unwritten("route", route.Identity)
	}
	return nil
}

func routeEqual(held, want caddyRoute) bool {
	written, err := json.Marshal([]caddyRoute{held, want})
	if err != nil {
		return false
	}
	var pair []json.RawMessage
	if err := json.Unmarshal(written, &pair); err != nil || len(pair) != 2 {
		return false
	}
	return string(pair[0]) == string(pair[1])
}

func previewBaseRead(read caddyConfig, entry map[string]string) (string, error) {
	base := entry[edge.PreviewEntryOwner]
	if probe := entry[edge.LivenessProbeLabel]; probe != base {
		return "", providerkit.Refuse(providerkit.CodeInvalid,
			"%s has a preview catch-all for %q but a liveness probe route for %q",
			ProxyConfig, base, probe)
	}
	held := read.Apps.HTTP.Servers[proxyServer].Automatic
	var skipped []string
	if held != nil {
		skipped = held.SkipCertificates
	}
	want := skipping(base)
	var wanted []string
	if want != nil {
		wanted = want.SkipCertificates
	}
	if !slices.Equal(skipped, wanted) {
		return "", providerkit.Refuse(providerkit.CodeInvalid,
			"%s skips %v from automatic https; the preview entry needs exactly %v",
			ProxyConfig, skipped, wanted)
	}
	return base, nil
}

func pinnedBy(read caddyConfig) ([]Pin, error) {
	if read.Apps.TLS == nil {
		return nil, nil
	}
	var pins []Pin
	for _, file := range read.Apps.TLS.Certificates.LoadFiles {
		mounted, cut := strings.CutSuffix(file.Certificate, pinCertificate)
		at, beneath := pinnedAt(mounted)
		if len(file.Tags) == 0 || !cut || !beneath || file.Key != PinKey(mounted) {
			return nil, unwritten("pinned certificate", file.Certificate)
		}
		for _, hostname := range file.Tags {
			pins = append(pins, Pin{Hostname: hostname, Path: at})
		}
	}
	return pins, nil
}

func ReadProxyState(document []byte) (ProxyState, error) {
	read, err := parsed(document)
	if err != nil {
		return ProxyState{}, err
	}
	grace, err := time.ParseDuration(read.Apps.HTTP.GracePeriod)
	if err != nil {
		return ProxyState{}, providerkit.Refuse(providerkit.CodeInvalid,
			"%s declares an unreadable grace period %q",
			ProxyConfig, read.Apps.HTTP.GracePeriod)
	}
	for _, named := range slices.Sorted(maps.Keys(read.Apps.HTTP.Servers)) {
		if named != proxyServer && named != proxyDrainServer {
			return ProxyState{}, unwritten("server", named)
		}
	}
	pins, err := pinnedBy(read)
	if err != nil {
		return ProxyState{}, err
	}
	if read.Apps.TLS != nil && len(read.Apps.TLS.Automation) > 0 {
		return ProxyState{}, providerkit.Refuse(providerkit.CodeInvalid,
			"%s declares a tls automation policy ocel did not write",
			ProxyConfig)
	}
	state := ProxyState{Grace: grace, Pins: pins}
	if draining, declared := read.Apps.HTTP.Servers[proxyDrainServer]; declared {
		if len(draining.Routes) != 1 || draining.Routes[0].Identity != drainIdentity {
			return ProxyState{}, unwritten("server", proxyDrainServer)
		}
		retiring, health, err := forwardedBy(draining.Routes[0])
		if err != nil {
			return ProxyState{}, err
		}
		if health != "" {
			return ProxyState{}, unwritten("route", drainIdentity)
		}
		state.Retiring = retiring
	}
	entry := map[string]string{}
	for _, route := range read.Apps.HTTP.Servers[proxyServer].Routes {
		if route.Identity == boxIdentity {
			continue
		}
		if route.Identity == connectorRoute {
			hosts := connectorHostOf(route)
			if hosts == "" || !routeEqual(route, connectorForwarding(hosts)) {
				return ProxyState{}, unwritten("route", route.Identity)
			}
			state.Connector = hosts
			continue
		}
		if base, named := previewRouteOf(route); named != "" {
			if err := previewRouteRead(route, base, named); err != nil {
				return ProxyState{}, err
			}
			entry[named] = base
			continue
		}
		if strings.HasPrefix(route.Identity, live.ClaimPrefix) {
			claim, named := live.ClaimedAt(route.Identity)
			if !named || !claimsOnly(route, claim.Hostname) {
				return ProxyState{}, unwritten("route", route.Identity)
			}
			state.Claims = append(state.Claims, HostClaim{
				Hostname: claim.Hostname, Owner: claim.Owner, Pointer: claim.Pointer, App: claim.App,
			})
			continue
		}
		if held, refuses := strings.CutSuffix(route.Identity, storeAdminSuffix); refuses && strings.HasPrefix(held, routeIdentity) {
			if !refusesStoreAdmin(route) {
				return ProxyState{}, unwritten("route", route.Identity)
			}
			continue
		}
		named, keyed := strings.CutPrefix(route.Identity, routeIdentity)
		fields := strings.Split(named, claimSeparator)
		if !keyed || len(fields) != 3 {
			return ProxyState{}, unwritten("route", route.Identity)
		}
		upstreams, health, err := forwardedBy(route)
		if err != nil {
			return ProxyState{}, err
		}
		if len(upstreams) != 1 {
			return ProxyState{}, misshapen(route.Identity, fmt.Sprintf("%d upstreams", len(upstreams)))
		}
		state.Routes = append(state.Routes, AppRoute{
			RouteKey: RouteKey{Owner: fields[0], Pointer: fields[1], App: fields[2]},
			Upstream: upstreams[0],
			Health:   health,
		})
	}
	base, err := previewBaseRead(read, entry)
	if err != nil {
		return ProxyState{}, err
	}
	state.PreviewBase = base
	return state, nil
}

func forwardedBy(route caddyRoute) ([]string, string, error) {
	if len(route.Handle) != 2 {
		return nil, "", misshapen(route.Identity, fmt.Sprintf("%d handlers", len(route.Handle)))
	}
	naming, forwards := route.Handle[0], route.Handle[1]
	edged := naming.Response != nil && maps.EqualFunc(naming.Response.Set, map[string][]string{EdgeHeader: {EdgeName}}, slices.Equal)
	switch {
	case naming.Handler != edgeHandler || !edged || len(naming.Upstreams) > 0 || naming.Status != 0 || len(naming.Headers) > 0:
		return nil, "", misshapen(route.Identity, fmt.Sprintf("a leading %q handler not setting only %s: %s", naming.Handler, EdgeHeader, EdgeName))
	case forwards.Handler != forwardHandler || forwards.Status != 0 || len(forwards.Headers) > 0 || forwards.Response != nil:
		return nil, "", misshapen(route.Identity, fmt.Sprintf("a terminal %q handler", forwards.Handler))
	case len(forwards.Upstreams) == 0:
		return nil, "", misshapen(route.Identity, "no upstreams")
	}
	dials := make([]string, 0, len(forwards.Upstreams))
	for _, upstream := range forwards.Upstreams {
		if upstream.Dial == "" {
			return nil, "", misshapen(route.Identity, "an upstream naming nothing to dial")
		}
		dials = append(dials, upstream.Dial)
	}
	health := ""
	if forwards.HealthChecks != nil {
		health = forwards.HealthChecks.Active.URI
		if health == "" || !routeEqual(caddyRoute{Handle: []caddyForward{{HealthChecks: forwards.HealthChecks}}}, caddyRoute{Handle: []caddyForward{{HealthChecks: checking(health)}}}) {
			return nil, "", misshapen(route.Identity, "an unexpected active health check")
		}
	}
	return dials, health, nil
}

func misshapen(named, found string) error {
	return providerkit.Refuse(providerkit.CodeInvalid,
		"%s has route %q with %s, which ocel did not write",
		ProxyConfig, named, found)
}

func forwardedTo(route caddyRoute) string {
	for _, handled := range route.Handle {
		if len(handled.Upstreams) > 0 {
			return handled.Upstreams[0].Dial
		}
	}
	return ""
}

func byKey(a, b AppRoute) int {
	return strings.Compare(a.identity(), b.identity())
}

func bySurface(a, b surfaceKey) int {
	return strings.Compare(a.Owner+claimSeparator+a.Pointer, b.Owner+claimSeparator+b.Pointer)
}

func validRoute(route AppRoute) error {
	for _, field := range []struct{ what, named string }{
		{"surface", route.Owner}, {"pointer", route.Pointer}, {"app", route.App},
	} {
		what, named := field.what, field.named
		if named == "" || strings.Contains(named, claimSeparator) {
			return providerkit.Refuse(providerkit.CodeInvalid,
				"a route on this box has an invalid %s %q",
				what, named)
		}
	}
	if route.Upstream == "" {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"the route %s names no upstream to forward to", route.identity())
	}
	if route.Health != "" && !strings.HasPrefix(route.Health, "/") {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"the route %s has health path %q, which does not start with /", route.identity(), route.Health)
	}
	return nil
}

func unwritten(what, named string) error {
	return providerkit.Refuse(providerkit.CodeInvalid,
		"%s carries a %s ocel did not write: %q",
		ProxyConfig, what, named)
}

func claimsOnly(route caddyRoute, hostname string) bool {
	return len(route.Handle) == 0 && len(route.Match) == 1 && slices.Equal(route.Match[0].hosts(), []string{hostname})
}

func validClaim(claim HostClaim) error {
	switch {
	case claim.Hostname == "" || claim.Owner == "" || claim.Pointer == "":
		return providerkit.Refuse(providerkit.CodeInvalid,
			"a hostname claim on this box is incomplete: host %q, surface %q, pointer %q",
			claim.Hostname, claim.Owner, claim.Pointer)
	case strings.Contains(claim.Hostname, "*"):
		return providerkit.Refuse(providerkit.CodeInvalid,
			"a hostname claim on this box names wildcard %q",
			claim.Hostname)
	case strings.Contains(claim.Owner, claimSeparator) || strings.Contains(claim.Hostname, claimSeparator) ||
		strings.Contains(claim.Pointer, claimSeparator) || strings.Contains(claim.App, claimSeparator):
		return providerkit.Refuse(providerkit.CodeInvalid,
			"the claim of %q by %q under pointer %q and app %q contains %q",
			claim.Hostname, claim.Owner, claim.Pointer, claim.App, claimSeparator)
	}
	return nil
}

func seededConfig() (caddyConfig, error) {
	read, err := parsed(proxyBaseline)
	if err != nil {
		return caddyConfig{}, err
	}
	if _, seeded := read.Apps.HTTP.Servers[proxyServer]; !seeded {
		return caddyConfig{}, providerkit.Refuse(providerkit.CodeInvalid,
			"the baseline config declares no %s server", proxyServer)
	}
	return read, nil
}

func parsed(document []byte) (caddyConfig, error) {
	var read caddyConfig
	if err := json.Unmarshal(document, &read); err != nil {
		return caddyConfig{}, providerkit.Refuse(providerkit.CodeInvalid,
			"%s is not valid json: %v", ProxyConfig, err)
	}
	return read, nil
}

func spelled(window time.Duration) string {
	return fmt.Sprintf("%ds", int(window.Round(time.Second).Seconds()))
}
