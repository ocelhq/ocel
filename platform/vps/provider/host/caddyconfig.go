package host

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/caddyadmin"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
)

const (
	proxyServer      = "ocel"
	proxyDrainServer = "ocel_drain"
	drainListen      = "127.0.0.1:9"
	routeIdentity    = "ocel-app-"
	claimIdentity    = "ocel-host-"
	claimSeparator   = "/"
	drainIdentity    = "ocel-retiring"
	boxIdentity      = "ocel-box"
)

const (
	EdgeHeader = "X-Ocel-Edge"
	EdgeName   = "box"
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
}

type HostClaim struct {
	Hostname string
	Owner    string
	App      string
}

type claimKey struct {
	Owner string
	App   string
}

func (c HostClaim) key() claimKey { return claimKey{Owner: c.Owner, App: c.App} }

func (c HostClaim) identity() string {
	fields := []string{c.Owner, c.Hostname}
	if c.App != "" {
		fields = append(fields, c.App)
	}
	return claimIdentity + strings.Join(fields, claimSeparator)
}

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
	Grace    time.Duration
	Claims   []HostClaim
	Routes   []AppRoute
	Pins     []Pin
	Retiring string
}

func Claiming(claims []HostClaim, taken HostClaim) ([]HostClaim, error) {
	if at := slices.IndexFunc(claims, func(claim HostClaim) bool { return claim.Hostname == taken.Hostname }); at >= 0 && claims[at].Owner != taken.Owner {
		return nil, providerkit.Refuse(providerkit.CodeBusy,
			"%s is claimed on this box by %s, and one hostname answers for one surface: unbind it there before %s binds it, or this bind would take a live site off the air with nothing telling the project that lost it",
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
	HTTP caddyHTTP `json:"http"`
	TLS  *caddyTLS `json:"tls,omitempty"`
}

type caddyTLS struct {
	Certificates caddyCertificates `json:"certificates"`
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
	Listen []string        `json:"listen"`
	Logs   json.RawMessage `json:"logs,omitempty"`
	Routes []caddyRoute    `json:"routes"`
}

type caddyRoute struct {
	Identity string         `json:"@id,omitempty"`
	Match    []caddyMatch   `json:"match,omitempty"`
	Handle   []caddyForward `json:"handle"`
}

type caddyMatch struct {
	Host []string `json:"host"`
}

type caddyForward struct {
	Handler   string              `json:"handler"`
	Upstreams []caddyDial         `json:"upstreams,omitempty"`
	Status    int                 `json:"status_code,omitempty"`
	Headers   map[string][]string `json:"headers,omitempty"`
	Response  *caddyHeaderOps     `json:"response,omitempty"`
}

type caddyHeaderOps struct {
	Set map[string][]string `json:"set"`
}

type caddyDial struct {
	Dial string `json:"dial"`
}

func namingTheEdge() caddyForward {
	return caddyForward{
		Handler:  "headers",
		Response: &caddyHeaderOps{Set: map[string][]string{EdgeHeader: {EdgeName}}},
	}
}

func forwarding(identity, upstream string) caddyRoute {
	return caddyRoute{
		Identity: identity,
		Handle: []caddyForward{namingTheEdge(), {
			Handler:   "reverse_proxy",
			Upstreams: []caddyDial{{Dial: upstream}},
		}},
	}
}

func refusing(identity string) caddyRoute {
	return caddyRoute{
		Identity: identity,
		Handle: []caddyForward{{
			Handler: "static_response",
			Status:  http.StatusNotFound,
			Headers: map[string][]string{EdgeHeader: {EdgeName}},
		}},
	}
}

func matching(identity string, hostnames []string, upstream string) caddyRoute {
	route := forwarding(identity, upstream)
	route.Match = []caddyMatch{{Host: append([]string{}, hostnames...)}}
	return route
}

func RenderProxyConfig(state ProxyState) ([]byte, error) {
	seeded, err := seededConfig()
	if err != nil {
		return nil, err
	}
	routes := make([]caddyRoute, 0, len(state.Claims)+len(state.Routes)+1)
	claimed := map[claimKey][]string{}
	for _, claim := range slices.SortedFunc(slices.Values(state.Claims), func(a, b HostClaim) int {
		return strings.Compare(a.Hostname, b.Hostname)
	}) {
		if err := validClaim(claim); err != nil {
			return nil, err
		}
		routes = append(routes, caddyRoute{
			Identity: claim.identity(),
			Match:    []caddyMatch{{Host: []string{claim.Hostname}}},
			Handle:   []caddyForward{},
		})
		claimed[claim.key()] = append(claimed[claim.key()], claim.Hostname)
	}
	standing := slices.SortedFunc(slices.Values(state.Routes), byKey)
	running := map[string][]string{}
	for _, route := range standing {
		if err := validRoute(route); err != nil {
			return nil, err
		}
		if !slices.Contains(running[route.Owner], route.App) {
			running[route.Owner] = append(running[route.Owner], route.App)
		}
	}
	for _, owner := range slices.Sorted(maps.Keys(running)) {
		wide := claimed[claimKey{Owner: owner}]
		if len(wide) == 0 || len(running[owner]) == 1 {
			continue
		}
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"%s claims %s on this box and runs %s: a reverse proxy handler is terminal, so whichever app sorted first would answer every one of those hostnames and the rest would be configuration nothing reaches. Declare the hostname under the app that serves it — domains.production on the app rather than on the project — and run this again",
			owner, strings.Join(wide, ", "), strings.Join(running[owner], " and "))
	}
	answering := map[string]AppRoute{}
	for _, route := range standing {
		hostnames := claimed[claimKey{Owner: route.Owner, App: route.App}]
		if len(running[route.Owner]) == 1 {
			hostnames = append(slices.Clone(hostnames), claimed[claimKey{Owner: route.Owner}]...)
		}
		slices.Sort(hostnames)
		for _, hostname := range hostnames {
			if held, taken := answering[hostname]; taken {
				return nil, providerkit.Refuse(providerkit.CodeInvalid,
					"%s is claimed on this box and would be forwarded by %s and %s alike: a reverse proxy handler is terminal, so the second of those routes is configuration nothing reaches",
					hostname, held.identity(), route.identity())
			}
			answering[hostname] = route
		}
		routes = append(routes, matching(route.identity(), hostnames, route.Upstream))
	}
	routes = append(routes, refusing(boxIdentity))

	live := seeded.Apps.HTTP.Servers[proxyServer]
	live.Routes = routes
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
		},
	}
	if state.Retiring != "" {
		rendered.Apps.HTTP.Servers[proxyDrainServer] = caddyServer{
			Listen: []string{drainListen},
			Routes: []caddyRoute{forwarding(drainIdentity, state.Retiring)},
		}
	}
	return json.Marshal(rendered)
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
			"a pinned certificate on this box names host %q at %q, and the proxy loads a pair off %s/<name> alone: %s is the one directory bound into the proxy, and a pair anywhere else on this host is a path the proxy cannot open",
			pin.Hostname, pin.Path, ProxyPins, ProxyPins)
	}
	return nil
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
			"%s declares its grace period as %q, and the deploy's drain ceiling and the proxy's are one number",
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
	state := ProxyState{Grace: grace, Pins: pins}
	for _, route := range read.Apps.HTTP.Servers[proxyServer].Routes {
		if route.Identity == boxIdentity {
			continue
		}
		if claim, mine := strings.CutPrefix(route.Identity, claimIdentity); mine {
			fields := strings.Split(claim, claimSeparator)
			if len(fields) < 2 || len(fields) > 3 || slices.Contains(fields, "") || !claimsOnly(route, fields[1]) {
				return ProxyState{}, unwritten("route", route.Identity)
			}
			held := HostClaim{Hostname: fields[1], Owner: fields[0]}
			if len(fields) == 3 {
				held.App = fields[2]
			}
			state.Claims = append(state.Claims, held)
			continue
		}
		named, keyed := strings.CutPrefix(route.Identity, routeIdentity)
		fields := strings.Split(named, claimSeparator)
		upstream := forwardedTo(route)
		if !keyed || len(fields) != 3 || upstream == "" {
			return ProxyState{}, unwritten("route", route.Identity)
		}
		state.Routes = append(state.Routes, AppRoute{
			RouteKey: RouteKey{Owner: fields[0], Pointer: fields[1], App: fields[2]},
			Upstream: upstream,
		})
	}
	return state, nil
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

func validRoute(route AppRoute) error {
	for _, field := range []struct{ what, named string }{
		{"surface", route.Owner}, {"pointer", route.Pointer}, {"app", route.App},
	} {
		what, named := field.what, field.named
		if named == "" || strings.Contains(named, claimSeparator) {
			return providerkit.Refuse(providerkit.CodeInvalid,
				"a route on this box names %s %q, and %s tells one surface's route from another's out of the surface, the pointer and the app together",
				what, named, ProxyConfig)
		}
	}
	if route.Upstream == "" {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"the route %s names no upstream to forward to", route.identity())
	}
	return nil
}

func unwritten(what, named string) error {
	return providerkit.Refuse(providerkit.CodeInvalid,
		"%s carries a %s ocel did not write (%q), and a deploy that rewrites this file whole would take it with it",
		ProxyConfig, what, named)
}

func claimsOnly(route caddyRoute, hostname string) bool {
	return len(route.Handle) == 0 && len(route.Match) == 1 && slices.Equal(route.Match[0].Host, []string{hostname})
}

func validClaim(claim HostClaim) error {
	switch {
	case claim.Hostname == "" || claim.Owner == "":
		return providerkit.Refuse(providerkit.CodeInvalid,
			"a hostname claim on this box names host %q and surface %q, and %s answers which surface claims a host out of both",
			claim.Hostname, claim.Owner, ProxyConfig)
	case strings.Contains(claim.Owner, claimSeparator) || strings.Contains(claim.Hostname, claimSeparator) || strings.Contains(claim.App, claimSeparator):
		return providerkit.Refuse(providerkit.CodeInvalid,
			"the claim of %q by %q under app %q carries %q, which is what separates the surface, the host it claims and the app it was declared under",
			claim.Hostname, claim.Owner, claim.App, claimSeparator)
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
			"the baseline config declares no %s server, and every route a deploy renders hangs off it", proxyServer)
	}
	return read, nil
}

func parsed(document []byte) (caddyConfig, error) {
	var read caddyConfig
	if err := json.Unmarshal(document, &read); err != nil {
		return caddyConfig{}, providerkit.Refuse(providerkit.CodeInvalid,
			"%s is not the json a proxy configuration is read out of: %v", ProxyConfig, err)
	}
	return read, nil
}

func spelled(window time.Duration) string {
	return fmt.Sprintf("%ds", int(window.Round(time.Second).Seconds()))
}
