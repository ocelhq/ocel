package host

import (
	"cmp"
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
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const (
	proxyServer    = "ocel"
	routeIdentity  = "ocel-app-"
	claimSeparator = live.ClaimSeparator
	boxIdentity    = "ocel-box"
	connectorRoute = "ocel-connector"
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
	refuseHandler  = "static_response"
	rewriteHandler = "rewrite"
)

const (
	DeployWindow = 60 * time.Second
	DrainWindow  = 30 * time.Second
)

type RouteKey struct {
	Owner   string `json:"owner"`
	Pointer string `json:"pointer"`
	App     string `json:"app"`
}

func (k RouteKey) identity() string {
	return routeIdentity + strings.Join([]string{k.Owner, k.Pointer, k.App}, claimSeparator)
}

type AppRoute struct {
	RouteKey
	Upstream string `json:"upstream"`
	Health   string `json:"health,omitempty"`
}

const (
	healthInterval = 10 * time.Second
	healthTimeout  = 5 * time.Second
	healthExpects  = 2
)

type HostClaim live.Claimed

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
	return live.ClaimIdentity(live.Claimed(c))
}

func (r AppRoute) app() bool { return r.App != live.StoreLabel }

type Pin struct {
	Hostname string `json:"hostname"`
	Path     string `json:"path"`
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
	StreamDelay     string              `json:"stream_close_delay,omitempty"`
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

func forwarding(identity, upstream string, grace time.Duration) caddyRoute {
	return caddyRoute{
		Identity: identity,
		Handle:   []caddyForward{namingTheEdge(), forwardingTo(upstream, grace)},
	}
}

func forwardingTo(upstream string, grace time.Duration) caddyForward {
	return caddyForward{
		Handler:     caddyadmin.ForwardHandler,
		Upstreams:   []caddyDial{{Dial: upstream}},
		StreamDelay: spelled(grace),
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

func connectorForwarding(hostname string, grace time.Duration) caddyRoute {
	return caddyRoute{
		Identity: connectorRoute,
		Match:    []caddyMatch{{Host: hostnamesOf(hostname), Path: []string{ConnectorPath, ConnectorPath + "/*"}}},
		Handle: []caddyForward{
			namingTheEdge(),
			{Handler: rewriteHandler, StripPathPrefix: ConnectorPath},
			forwardingTo(ConnectorDial, grace),
		},
	}
}

func matching(app AppRoute, hostnames []string, grace time.Duration) caddyRoute {
	route := forwarding(app.identity(), app.Upstream, grace)
	route.Handle[1].HealthChecks = checking(app.Health)
	route.Match = []caddyMatch{{Host: hostnamesOf(hostnames...)}}
	return route
}

func RenderProxyConfig(state RoutingTable) ([]byte, error) {
	seeded, err := seededConfig()
	if err != nil {
		return nil, err
	}
	routes := make([]caddyRoute, 0, len(state.Claims)+len(state.Routes)+2)
	if state.Connector != "" {
		routes = append(routes, connectorForwarding(state.Connector, state.Grace))
	}
	claimed := map[claimKey][]string{}
	for _, claim := range slices.SortedFunc(slices.Values(state.Claims), byClaimed) {
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
			named := strings.ToLower(hostname)
			if held, taken := answering[named]; taken {
				return nil, providerkit.Refuse(providerkit.CodeInvalid,
					"%s would be forwarded by both %s and %s",
					hostname, held.identity(), route.identity())
			}
			answering[named] = route
		}
		if !route.app() && len(hostnames) > 0 {
			routes = append(routes, refusingStoreAdmin(route, hostnames))
		}
		routes = append(routes, matching(route, hostnames, state.Grace))
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
	for _, pin := range slices.SortedFunc(slices.Values(pins), byPinned) {
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

func byClaimed(a, b HostClaim) int {
	return strings.Compare(a.Hostname, b.Hostname)
}

func byPinned(a, b Pin) int {
	return cmp.Or(strings.Compare(a.Hostname, b.Hostname), strings.Compare(a.Path, b.Path))
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
	if _, err := switchboard.UpstreamAddress(route.Upstream); err != nil {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"the route %s forwards to no upstream it can dial: %v", route.identity(), err)
	}
	if route.Health != "" && !strings.HasPrefix(route.Health, "/") {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"the route %s has health path %q, which does not start with /", route.identity(), route.Health)
	}
	return nil
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
