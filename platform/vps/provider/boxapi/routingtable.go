package boxapi

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/naming"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/gate"
)

const (
	StoreLabel     = "storage"
	ClaimSeparator = "/"
	RouteIdentity  = "ocel-app-"
)

const (
	ConnectorPath   = "/" + buildoutput.StateDir + "/connector"
	ConnectorSocket = "/run/ocel/connector.sock"
	ConnectorDial   = "unix/" + ConnectorSocket
)

var storeAdminPaths = []string{"/rustfs", "/health"}

type Table struct {
	Grace     time.Duration
	answering map[string]answer
	connector string
	Routed    map[string]bool
	admitted  map[string]bool
	tunneled  TunneledHostnames
	Visitor   visitorHeaders
	gates     map[string]*gate.Gate
}

type visitorHeaders struct {
	Address string
	scheme  string
}

type answer struct {
	upstream string
	store    bool
	gate     *gate.Gate
}

type Forward struct {
	Upstream string
	Strip    string
	Gate     *gate.Gate
}

type claim struct {
	Owner    string `json:"owner"`
	Hostname string `json:"hostname"`
	Pointer  string `json:"pointer"`
	App      string `json:"app,omitempty"`
}

type shield struct {
	Hostname          string   `json:"hostname"`
	Owner             string   `json:"owner"`
	ClientCAs         []string `json:"clientCAs,omitempty"`
	OriginCertificate struct {
		Certificate string `json:"certificate"`
		Key         string `json:"key"`
	} `json:"originCertificate"`
}

type route struct {
	Owner    string `json:"owner"`
	Pointer  string `json:"pointer"`
	App      string `json:"app"`
	Upstream string `json:"upstream"`
}

func (r route) identity() string {
	return RouteIdentity + strings.Join([]string{r.Owner, r.Pointer, r.App}, ClaimSeparator)
}

func (r route) app() bool { return r.App != StoreLabel }

func (r route) surface() surfaceKey { return surfaceKey{r.Owner, r.Pointer} }

func (r route) key() claimKey { return claimKey{r.Owner, r.Pointer, r.App} }

type pin struct {
	Hostname string `json:"hostname"`
	Path     string `json:"path"`
}

type writtenTable struct {
	Grace       string     `json:"grace"`
	Claims      []claim    `json:"claims,omitempty"`
	Routes      []route    `json:"routes,omitempty"`
	Pins        []pin      `json:"pins,omitempty"`
	Shields     []shield   `json:"shields,omitempty"`
	PreviewBase string     `json:"preview,omitempty"`
	Connector   string     `json:"connector,omitempty"`
	Tunneled    []tunneled `json:"tunneled,omitempty"`
	Tunnel      *tunnel    `json:"tunnel,omitempty"`
	Retired     []tunnel   `json:"retired,omitempty"`
	Gates       []heldGate `json:"gates,omitempty"`
}

type heldGate struct {
	Owner string    `json:"owner"`
	Gate  gate.Gate `json:"gate"`
}

type tunneled struct {
	Hostname string `json:"hostname"`
	Owner    string `json:"owner"`
}

type tunnel struct {
	Edge                 string `json:"edge"`
	Name                 string `json:"name"`
	ID                   string `json:"id,omitempty"`
	Address              string `json:"address,omitempty"`
	VisitorAddressHeader string `json:"visitorAddressHeader,omitempty"`
	VisitorSchemeHeader  string `json:"visitorSchemeHeader,omitempty"`
}

type claimKey struct{ owner, pointer, app string }

type surfaceKey struct{ owner, pointer string }

func ReadTable(document []byte) (*Table, error) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var read writtenTable
	if err := decoder.Decode(&read); err != nil {
		return nil, fmt.Errorf("the routing table is not one ocel wrote: %w", err)
	}
	if decoder.More() {
		return nil, fmt.Errorf("the routing table contains more than one table")
	}
	grace, err := time.ParseDuration(read.Grace)
	if err != nil {
		return nil, fmt.Errorf("the routing table's grace %q is not a duration: %w", read.Grace, err)
	}
	lowered(&read)
	if read.PreviewBase != "" {
		if err := edge.RefuseUnusablePreviewBase(read.PreviewBase); err != nil {
			return nil, err
		}
	}
	claimed := map[claimKey][]string{}
	for _, hostClaim := range slices.SortedFunc(slices.Values(read.Claims), byHostname) {
		if err := hostClaim.valid(); err != nil {
			return nil, err
		}
		key := claimKey{hostClaim.Owner, hostClaim.Pointer, hostClaim.App}
		claimed[key] = append(claimed[key], hostClaim.Hostname)
	}
	for _, held := range read.Shields {
		if held.Hostname == "" || held.Owner == "" || len(held.ClientCAs) == 0 {
			return nil, fmt.Errorf("a shield is incomplete: host %q, surface %q, %d client certificates",
				held.Hostname, held.Owner, len(held.ClientCAs))
		}
	}
	gates := map[string]*gate.Gate{}
	for _, held := range read.Gates {
		if held.Owner == "" || held.Gate.Key == "" {
			return nil, fmt.Errorf("a gate is incomplete: surface %q, key held: %t", held.Owner, held.Gate.Key != "")
		}
		if _, twice := gates[held.Owner]; twice {
			return nil, fmt.Errorf("%s holds more than one gate", held.Owner)
		}
		gates[held.Owner] = &held.Gate
	}
	routes := slices.SortedFunc(slices.Values(read.Routes), byIdentity)
	running := map[surfaceKey][]string{}
	for at, appRoute := range routes {
		address, err := appRoute.valid()
		if err != nil {
			return nil, err
		}
		routes[at].Upstream = address
		if appRoute.app() && !slices.Contains(running[appRoute.surface()], appRoute.App) {
			running[appRoute.surface()] = append(running[appRoute.surface()], appRoute.App)
		}
	}
	for _, serving := range slices.SortedFunc(maps.Keys(running), bySurface) {
		wide := claimed[claimKey{serving.owner, serving.pointer, ""}]
		if len(wide) > 0 && len(running[serving]) > 1 {
			return nil, fmt.Errorf("%s claims %s under %s, which runs %s\nDeclare domains.production on the app that serves it, not the project",
				serving.owner, strings.Join(wide, ", "), serving.pointer, strings.Join(running[serving], " and "))
		}
	}
	table := &Table{
		Grace:     grace,
		answering: map[string]answer{},
		connector: read.Connector,
		Routed:    map[string]bool{},
		admitted:  map[string]bool{},
		gates:     gates,
	}
	hostnames := make([]string, 0, len(read.Tunneled))
	for _, held := range read.Tunneled {
		hostnames = append(hostnames, held.Hostname)
	}
	table.tunneled = NewTunneledHostnames(hostnames)
	if read.Tunnel != nil {
		table.Visitor = visitorHeaders{Address: read.Tunnel.VisitorAddressHeader, scheme: read.Tunnel.VisitorSchemeHeader}
	}
	for _, hostClaim := range read.Claims {
		table.admitted[hostClaim.Hostname] = true
	}
	if table.connector != "" {
		table.admitted[table.connector] = true
	}
	if read.PreviewBase != "" {
		table.admitted[edge.ProbeHostname(edge.PreviewWildcard(read.PreviewBase))] = true
	}
	pinned := map[string]bool{}
	for _, pin := range read.Pins {
		pinned[pin.Hostname] = true
	}
	for _, held := range read.Shields {
		if held.OriginCertificate.Certificate != "" {
			pinned[strings.ToLower(held.Hostname)] = true
		}
	}
	for hostname := range table.admitted {
		if pinned[hostname] || pinned[wildcardOver(hostname)] || table.IsTunneled(hostname) {
			delete(table.admitted, hostname)
		}
	}
	answeredBy := map[string]route{}
	for _, appRoute := range routes {
		hostnames := claimed[appRoute.key()]
		if appRoute.app() && len(running[appRoute.surface()]) == 1 {
			hostnames = append(slices.Clone(hostnames), claimed[claimKey{appRoute.Owner, appRoute.Pointer, ""}]...)
		}
		slices.Sort(hostnames)
		for _, hostname := range hostnames {
			if first, answered := answeredBy[hostname]; answered {
				return nil, fmt.Errorf("%s would be forwarded by both %s and %s",
					hostname, first.identity(), appRoute.identity())
			}
			answeredBy[hostname] = appRoute
			answered := answer{upstream: appRoute.Upstream, store: !appRoute.app()}
			if appRoute.app() {
				answered.gate = gates[appRoute.Owner]
			}
			table.answering[hostname] = answered
		}
		table.Routed[appRoute.Upstream] = true
	}
	return table, nil
}

func lowered(read *writtenTable) {
	for at := range read.Claims {
		read.Claims[at].Hostname = strings.ToLower(read.Claims[at].Hostname)
	}
	for at := range read.Pins {
		read.Pins[at].Hostname = strings.ToLower(read.Pins[at].Hostname)
	}
	for at := range read.Tunneled {
		read.Tunneled[at].Hostname = strings.ToLower(read.Tunneled[at].Hostname)
	}
	read.PreviewBase = strings.ToLower(read.PreviewBase)
	read.Connector = strings.ToLower(read.Connector)
}

func (c claim) valid() error {
	switch {
	case c.Hostname == "" || c.Owner == "" || c.Pointer == "":
		return fmt.Errorf("a hostname claim is incomplete: host %q, surface %q, pointer %q",
			c.Hostname, c.Owner, c.Pointer)
	case strings.Contains(c.Hostname, "*"):
		return fmt.Errorf("a hostname claim names wildcard %q", c.Hostname)
	case strings.Contains(c.Owner, ClaimSeparator) || strings.Contains(c.Hostname, ClaimSeparator) ||
		strings.Contains(c.Pointer, ClaimSeparator) || strings.Contains(c.App, ClaimSeparator):
		return fmt.Errorf("the claim of %q by %q under pointer %q and app %q contains %q",
			c.Hostname, c.Owner, c.Pointer, c.App, ClaimSeparator)
	}
	return nil
}

func (r route) valid() (string, error) {
	for _, field := range []struct{ what, named string }{
		{"surface", r.Owner}, {"pointer", r.Pointer}, {"app", r.App},
	} {
		if field.named == "" || strings.Contains(field.named, ClaimSeparator) {
			return "", fmt.Errorf("a route has an invalid %s %q", field.what, field.named)
		}
	}
	address, err := UpstreamAddress(r.Upstream)
	if err != nil {
		return "", fmt.Errorf("the route %s forwards to no upstream it can dial: %w", r.identity(), err)
	}
	return address, nil
}

func UpstreamAddress(dial string) (string, error) {
	written := dial
	if network, rest, named := strings.Cut(dial, "/"); named {
		if !strings.EqualFold(strings.TrimSpace(network), "tcp") {
			return "", fmt.Errorf("%q is dialled over %s rather than tcp", dial, network)
		}
		written = rest
	}
	host, port, err := net.SplitHostPort(written)
	if err != nil {
		return "", fmt.Errorf("%q is not a host:port: %w", dial, err)
	}
	number, err := strconv.ParseUint(port, 10, 16)
	if err != nil || number == 0 || host == "" {
		return "", fmt.Errorf("%q does not name one host and one port", dial)
	}
	return net.JoinHostPort(host, strconv.FormatUint(number, 10)), nil
}

func (t *Table) Forward(host, requested string) (Forward, bool) {
	named := HostOf(host)
	if t.connector != "" && named == t.connector && Under(requested, ConnectorPath) {
		return Forward{Upstream: ConnectorDial, Strip: ConnectorPath}, true
	}
	answered, ok := t.answering[named]
	if !ok || answered.store && slices.ContainsFunc(storeAdminPaths, func(admin string) bool {
		return Under(requested, admin) || Under(path.Clean(requested), admin)
	}) {
		return Forward{}, false
	}
	return Forward{Upstream: answered.upstream, Gate: answered.gate}, true
}

func (t *Table) Admits(hostname string) bool { return t.admitted[strings.ToLower(hostname)] }

func (t *Table) IsTunneled(host string) bool { return t.tunneled.Has(host) }

type TunneledHostnames map[string]bool

func NewTunneledHostnames(hostnames []string) TunneledHostnames {
	tunneled := TunneledHostnames{}
	for _, hostname := range hostnames {
		tunneled[strings.ToLower(hostname)] = true
	}
	return tunneled
}

func (t TunneledHostnames) Has(host string) bool {
	named := HostOf(host)
	return t[named] || t[wildcardOver(named)]
}

func wildcardOver(hostname string) string {
	if _, parent, split := strings.Cut(hostname, "."); split {
		return "*." + parent
	}
	return ""
}

func Under(requested, prefix string) bool {
	lowered := strings.ToLower(requested)
	return lowered == prefix || strings.HasPrefix(lowered, prefix+"/")
}

func HostOf(host string) string {
	if named, _, err := net.SplitHostPort(host); err == nil {
		host = named
	}
	return strings.ToLower(host)
}

func byHostname(a, b claim) int { return strings.Compare(a.Hostname, b.Hostname) }

func byIdentity(a, b route) int { return strings.Compare(a.identity(), b.identity()) }

func bySurface(a, b surfaceKey) int {
	return cmp.Or(strings.Compare(a.owner, b.owner), strings.Compare(a.pointer, b.pointer))
}

func (v visitorHeaders) IsOverHTTP(r *http.Request) bool {
	if said := r.Header.Get("X-Forwarded-Proto"); said != "" {
		return strings.EqualFold(said, "http")
	}
	if v.scheme == "" {
		return false
	}
	var visitor struct {
		Scheme string `json:"scheme"`
	}
	return json.Unmarshal([]byte(r.Header.Get(v.scheme)), &visitor) == nil && strings.EqualFold(visitor.Scheme, "http")
}

const ClaimPrefix = "ocel-host-"

func Surface(slug, tier string) string {
	return naming.Join(naming.FieldSeparator, "ocel", naming.Sanitize(slug), tier)
}

func StoreHostname(hostname string) string {
	return StoreLabel + "." + hostname
}

type Claimed struct {
	Owner    string `json:"owner"`
	Hostname string `json:"hostname"`
	Pointer  string `json:"pointer"`
	App      string `json:"app,omitempty"`
}

func ClaimIdentity(c Claimed) string {
	fields := []string{c.Owner, c.Hostname, c.Pointer}
	if c.App != "" {
		fields = append(fields, c.App)
	}
	return ClaimPrefix + strings.Join(fields, ClaimSeparator)
}

func ClaimedIn(table []byte) ([]Claimed, error) {
	var read struct {
		Claims []Claimed `json:"claims"`
	}
	if err := json.Unmarshal(table, &read); err != nil {
		return nil, fmt.Errorf("read what this box claims: %w", err)
	}
	return read.Claims, nil
}

func StoreBase(claims []Claimed, owner, pointer string) string {
	hostnames := make([]string, 0, len(claims))
	for _, claim := range claims {
		if claim.Owner == owner && claim.Pointer == pointer && claim.App == StoreLabel {
			hostnames = append(hostnames, claim.Hostname)
		}
	}
	if len(hostnames) == 0 {
		return ""
	}
	slices.Sort(hostnames)
	return "https://" + hostnames[0]
}
