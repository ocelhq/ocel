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

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/naming"
)

const (
	StoreLabel     = "storage"
	ClaimSeparator = "/"
	RouteIdentity  = "ocel-app-"
	ClaimPrefix    = "ocel-host-"
)

const (
	ConnectorPath   = "/" + buildoutput.StateDir + "/connector"
	ConnectorSocket = "/run/ocel/connector.sock"
	ConnectorDial   = "unix/" + ConnectorSocket
)

var storeAdminPaths = []string{"/rustfs", "/health"}

type RoutingTable struct {
	Grace           time.Duration `json:"-"`
	Claims          []Claim       `json:"claims,omitempty"`
	Routes          []Route       `json:"routes,omitempty"`
	Pins            []Pin         `json:"pins,omitempty"`
	Shields         []Shield      `json:"shields,omitempty"`
	PreviewBase     string        `json:"preview,omitempty"`
	PreviewTunneled bool          `json:"previewTunneled,omitempty"`
	Connector       string        `json:"connector,omitempty"`
	Tunnel          *Tunnel       `json:"tunnel,omitempty"`
	Retired         []Tunnel      `json:"retired,omitempty"`
	Gates           []OwnedGate   `json:"gates,omitempty"`
	OriginID        string        `json:"originId,omitempty"`
}

type Claim struct {
	Owner    string `json:"owner"`
	Hostname string `json:"hostname"`
	Pointer  string `json:"pointer"`
	App      string `json:"app,omitempty"`
	Tunneled bool   `json:"tunneled,omitempty"`
}

type RouteKey struct {
	Owner   string `json:"owner"`
	Pointer string `json:"pointer"`
	App     string `json:"app"`
}

func (k RouteKey) Identity() string {
	return RouteIdentity + strings.Join([]string{k.Owner, k.Pointer, k.App}, ClaimSeparator)
}

type Route struct {
	RouteKey
	Upstream string `json:"upstream"`
}

func (r Route) isApp() bool { return r.App != StoreLabel }

type Pin struct {
	Project  string `json:"project"`
	Hostname string `json:"hostname"`
	Path     string `json:"path"`
}

type CertificatePair struct {
	Certificate string `json:"certificate"`
	Key         string `json:"key"`
}

func (p CertificatePair) Bundle() []byte {
	certificate := p.Certificate
	if !strings.HasSuffix(certificate, "\n") {
		certificate += "\n"
	}
	return []byte(certificate + p.Key)
}

type Shield struct {
	Hostname          string          `json:"hostname"`
	Owner             string          `json:"owner"`
	ClientCAs         []string        `json:"clientCAs,omitempty"`
	OriginCertificate CertificatePair `json:"originCertificate,omitzero"`
}

type OwnedGate struct {
	Owner string    `json:"owner"`
	Gate  gate.Gate `json:"gate"`
}

type Tunnel struct {
	Edge                 edge.Kind `json:"edge"`
	Name                 string    `json:"name"`
	ID                   string    `json:"id,omitempty"`
	Address              string    `json:"address,omitempty"`
	VisitorAddressHeader string    `json:"visitorAddressHeader,omitempty"`
	VisitorSchemeHeader  string    `json:"visitorSchemeHeader,omitempty"`
}

type routingRows RoutingTable

type writtenRoutingTable struct {
	Grace string `json:"grace"`
	routingRows
}

func WriteRoutingTable(table RoutingTable) ([]byte, error) {
	if err := table.Validate(); err != nil {
		return nil, err
	}
	sorted := table
	sorted.Claims = slices.SortedFunc(slices.Values(table.Claims), func(a, b Claim) int { return strings.Compare(a.Hostname, b.Hostname) })
	sorted.Routes = slices.SortedFunc(slices.Values(table.Routes), func(a, b Route) int { return strings.Compare(a.Identity(), b.Identity()) })
	sorted.Pins = slices.SortedFunc(slices.Values(table.Pins), func(a, b Pin) int {
		return cmp.Or(strings.Compare(a.Hostname, b.Hostname), strings.Compare(a.Path, b.Path), strings.Compare(a.Project, b.Project))
	})
	sorted.Shields = slices.SortedFunc(slices.Values(table.Shields), func(a, b Shield) int {
		return cmp.Or(strings.Compare(a.Hostname, b.Hostname), strings.Compare(a.Owner, b.Owner))
	})
	sorted.Gates = slices.SortedFunc(slices.Values(table.Gates), func(a, b OwnedGate) int { return strings.Compare(a.Owner, b.Owner) })
	return json.Marshal(writtenRoutingTable{Grace: spelledGrace(table.Grace), routingRows: routingRows(sorted)})
}

func spelledGrace(window time.Duration) string {
	return fmt.Sprintf("%ds", int(window.Round(time.Second).Seconds()))
}

func ReadRoutingTable(document []byte) (RoutingTable, error) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var read writtenRoutingTable
	if err := decoder.Decode(&read); err != nil {
		return RoutingTable{}, fmt.Errorf("the routing table is not one ocel wrote: %w", err)
	}
	if decoder.More() {
		return RoutingTable{}, fmt.Errorf("the routing table contains more than one table")
	}
	grace, err := time.ParseDuration(read.Grace)
	if err != nil {
		return RoutingTable{}, fmt.Errorf("the routing table's grace %q is not a duration: %w", read.Grace, err)
	}
	table := RoutingTable(read.routingRows)
	table.Grace = grace
	if err := table.Validate(); err != nil {
		return RoutingTable{}, err
	}
	return table, nil
}

func ReadTable(document []byte) (*Table, error) {
	table, err := ReadRoutingTable(document)
	if err != nil {
		return nil, err
	}
	return NewTable(table)
}

func (t RoutingTable) Validate() error {
	_, err := NewTable(t)
	return err
}

func (t RoutingTable) Hostnames() []string {
	tunneled := t.tunneled()
	named := make([]string, 0, len(t.Claims)+2)
	for _, claim := range t.Claims {
		if !tunneled.has(claim.Hostname) {
			named = append(named, claim.Hostname)
		}
	}
	if t.Connector != "" && !tunneled.has(t.Connector) {
		named = append(named, t.Connector)
	}
	if t.PreviewBase != "" && !t.PreviewTunneled {
		named = append(named, edge.ProbeHostname(edge.PreviewWildcard(t.PreviewBase)))
	}
	slices.Sort(named)
	return slices.Compact(named)
}

func (t RoutingTable) IsTunneled(host string) bool { return t.tunneled().has(host) }

func (t RoutingTable) HasTunneled() bool {
	return t.PreviewTunneled || slices.ContainsFunc(t.Claims, func(claim Claim) bool { return claim.Tunneled })
}

func (t RoutingTable) tunneled() hostnameSet {
	tunneled := hostnameSet{}
	for _, claim := range t.Claims {
		if claim.Tunneled {
			tunneled[strings.ToLower(claim.Hostname)] = true
		}
	}
	if t.PreviewTunneled && t.PreviewBase != "" {
		tunneled[strings.ToLower(edge.PreviewWildcard(t.PreviewBase))] = true
	}
	return tunneled
}

type hostnameSet map[string]bool

func (s hostnameSet) has(host string) bool {
	named := HostOf(host)
	return s[named] || s[wildcardOver(named)]
}

type Table struct {
	grace     time.Duration
	answering map[string]answer
	connector string
	routed    map[string]bool
	admitted  map[string]bool
	tunneled  hostnameSet
	visitor   visitorHeaders
}

type visitorHeaders struct {
	address string
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

type claimKey struct{ owner, pointer, app string }

type surfaceKey struct{ owner, pointer string }

func NewTable(read RoutingTable) (*Table, error) {
	preview := strings.ToLower(read.PreviewBase)
	if preview != "" {
		if err := edge.RefuseUnusablePreviewBase(preview); err != nil {
			return nil, err
		}
	}
	claimed := map[claimKey][]string{}
	for _, claim := range slices.SortedFunc(slices.Values(read.Claims), func(a, b Claim) int { return strings.Compare(a.Hostname, b.Hostname) }) {
		if err := claim.refuseInvalid(); err != nil {
			return nil, err
		}
		key := claimKey{claim.Owner, claim.Pointer, claim.App}
		claimed[key] = append(claimed[key], strings.ToLower(claim.Hostname))
	}
	for _, pin := range read.Pins {
		if _, pinned := Pinned(pin.Path); pin.Hostname == "" || !pinned {
			return nil, fmt.Errorf("pinned certificate for %q is at %q, not under %s/", pin.Hostname, pin.Path, PinsDir)
		}
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
	routes := slices.SortedFunc(slices.Values(read.Routes), func(a, b Route) int { return strings.Compare(a.Identity(), b.Identity()) })
	running := map[surfaceKey][]string{}
	for at, route := range routes {
		address, err := route.dialled()
		if err != nil {
			return nil, err
		}
		routes[at].Upstream = address
		surface := surfaceKey{route.Owner, route.Pointer}
		if route.isApp() && !slices.Contains(running[surface], route.App) {
			running[surface] = append(running[surface], route.App)
		}
	}
	for _, serving := range slices.SortedFunc(maps.Keys(running), func(a, b surfaceKey) int {
		return cmp.Or(strings.Compare(a.owner, b.owner), strings.Compare(a.pointer, b.pointer))
	}) {
		wide := claimed[claimKey{serving.owner, serving.pointer, ""}]
		if len(wide) > 0 && len(running[serving]) > 1 {
			return nil, fmt.Errorf("%s claims %s under %s, which runs %s\nDeclare domains.production on the app that serves it, not the project",
				serving.owner, strings.Join(wide, ", "), serving.pointer, strings.Join(running[serving], " and "))
		}
	}
	table := &Table{
		grace:     read.Grace,
		answering: map[string]answer{},
		connector: strings.ToLower(read.Connector),
		routed:    map[string]bool{},
		admitted:  map[string]bool{},
		tunneled:  read.tunneled(),
	}
	if read.Tunnel != nil {
		table.visitor = visitorHeaders{address: read.Tunnel.VisitorAddressHeader, scheme: read.Tunnel.VisitorSchemeHeader}
	}
	for _, claim := range read.Claims {
		table.admitted[strings.ToLower(claim.Hostname)] = true
	}
	if table.connector != "" {
		table.admitted[table.connector] = true
	}
	if preview != "" {
		table.admitted[edge.ProbeHostname(edge.PreviewWildcard(preview))] = true
	}
	pinned := map[string]bool{}
	for _, pin := range read.Pins {
		pinned[strings.ToLower(pin.Hostname)] = true
	}
	for _, held := range read.Shields {
		if held.OriginCertificate.Certificate != "" {
			pinned[strings.ToLower(held.Hostname)] = true
		}
	}
	for hostname := range table.admitted {
		if pinned[hostname] || pinned[wildcardOver(hostname)] || table.tunneled.has(hostname) {
			delete(table.admitted, hostname)
		}
	}
	answeredBy := map[string]Route{}
	for _, route := range routes {
		surface := surfaceKey{route.Owner, route.Pointer}
		hostnames := claimed[claimKey{route.Owner, route.Pointer, route.App}]
		if route.isApp() && len(running[surface]) == 1 {
			hostnames = append(slices.Clone(hostnames), claimed[claimKey{route.Owner, route.Pointer, ""}]...)
		}
		slices.Sort(hostnames)
		for _, hostname := range hostnames {
			if first, answered := answeredBy[hostname]; answered {
				return nil, fmt.Errorf("%s would be forwarded by both %s and %s",
					hostname, first.Identity(), route.Identity())
			}
			answeredBy[hostname] = route
			answered := answer{upstream: route.Upstream, store: !route.isApp()}
			if route.isApp() {
				answered.gate = gates[route.Owner]
			}
			table.answering[hostname] = answered
		}
		table.routed[route.Upstream] = true
	}
	return table, nil
}

func (c Claim) refuseInvalid() error {
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

func (r Route) dialled() (string, error) {
	for _, field := range []struct{ what, named string }{
		{"surface", r.Owner}, {"pointer", r.Pointer}, {"app", r.App},
	} {
		if field.named == "" || strings.Contains(field.named, ClaimSeparator) {
			return "", fmt.Errorf("a route has an invalid %s %q", field.what, field.named)
		}
	}
	address, err := UpstreamAddress(r.Upstream)
	if err != nil {
		return "", fmt.Errorf("the route %s forwards to no upstream it can dial: %w", r.Identity(), err)
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

func (t *Table) Grace() time.Duration { return t.grace }

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

func (t *Table) IsTunneled(host string) bool { return t.tunneled.has(host) }

func (t *Table) IsRouted(address string) bool { return t.routed[address] }

func (t *Table) Routed() []string { return slices.Sorted(maps.Keys(t.routed)) }

func (t *Table) VisitorAddressHeader() string { return t.visitor.address }

func (t *Table) IsOverHTTP(r *http.Request) bool {
	if said := r.Header.Get("X-Forwarded-Proto"); said != "" {
		return strings.EqualFold(said, "http")
	}
	if t.visitor.scheme == "" {
		return false
	}
	var visitor struct {
		Scheme string `json:"scheme"`
	}
	return json.Unmarshal([]byte(r.Header.Get(t.visitor.scheme)), &visitor) == nil && strings.EqualFold(visitor.Scheme, "http")
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

func Surface(slug, tier string) string {
	return naming.Join(naming.FieldSeparator, "ocel", naming.Sanitize(slug), tier)
}

func StoreHostname(hostname string) string {
	return StoreLabel + "." + hostname
}

func ClaimIdentity(c Claim) string {
	fields := []string{c.Owner, c.Hostname, c.Pointer}
	if c.App != "" {
		fields = append(fields, c.App)
	}
	return ClaimPrefix + strings.Join(fields, ClaimSeparator)
}

func StoreBase(claims []Claim, owner, pointer string) string {
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
