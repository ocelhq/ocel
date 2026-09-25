package switchboard

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/constants"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	storeLabel     = "storage"
	claimSeparator = "/"
	routeIdentity  = "ocel-app-"
)

const (
	ConnectorPath   = "/" + constants.ProjectStateDirName + "/connector"
	ConnectorSocket = "/run/ocel/connector.sock"
	connectorDial   = "unix/" + ConnectorSocket
)

var storeAdminPaths = []string{"/rustfs", "/health"}

type Table struct {
	grace     time.Duration
	answering map[string]answer
	connector string
	routed    map[string]bool
}

type answer struct {
	upstream string
	store    bool
}

type Forward struct {
	Upstream string
	Strip    string
}

type claim struct {
	Owner    string `json:"owner"`
	Hostname string `json:"hostname"`
	Pointer  string `json:"pointer"`
	App      string `json:"app,omitempty"`
}

type route struct {
	Owner    string `json:"owner"`
	Pointer  string `json:"pointer"`
	App      string `json:"app"`
	Upstream string `json:"upstream"`
}

func (r route) identity() string {
	return routeIdentity + strings.Join([]string{r.Owner, r.Pointer, r.App}, claimSeparator)
}

func (r route) app() bool { return r.App != storeLabel }

func (r route) surface() surfaceKey { return surfaceKey{r.Owner, r.Pointer} }

func (r route) key() claimKey { return claimKey{r.Owner, r.Pointer, r.App} }

type pin struct {
	Hostname string `json:"hostname"`
	Path     string `json:"path"`
}

type writtenTable struct {
	Grace       string  `json:"grace"`
	Claims      []claim `json:"claims,omitempty"`
	Routes      []route `json:"routes,omitempty"`
	Pins        []pin   `json:"pins,omitempty"`
	PreviewBase string  `json:"preview,omitempty"`
	Connector   string  `json:"connector,omitempty"`
}

type claimKey struct{ owner, pointer, app string }

type surfaceKey struct{ owner, pointer string }

func Read(document []byte) (*Table, error) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var read writtenTable
	if err := decoder.Decode(&read); err != nil {
		return nil, fmt.Errorf("the routing table is not one ocel wrote: %w", err)
	}
	if decoder.More() {
		return nil, fmt.Errorf("the routing table holds more than one table")
	}
	grace, err := time.ParseDuration(read.Grace)
	if err != nil {
		return nil, fmt.Errorf("the routing table's grace %q is not a duration: %w", read.Grace, err)
	}
	if read.PreviewBase != "" {
		if err := previewBaseUsable(read.PreviewBase); err != nil {
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
	standing := slices.SortedFunc(slices.Values(read.Routes), byIdentity)
	running := map[surfaceKey][]string{}
	for at, appRoute := range standing {
		address, err := appRoute.valid()
		if err != nil {
			return nil, err
		}
		standing[at].Upstream = address
		if appRoute.app() && !slices.Contains(running[appRoute.surface()], appRoute.App) {
			running[appRoute.surface()] = append(running[appRoute.surface()], appRoute.App)
		}
	}
	for _, serving := range slices.SortedFunc(maps.Keys(running), bySurface) {
		wide := claimed[claimKey{serving.owner, serving.pointer, ""}]
		if len(wide) > 0 && len(running[serving]) > 1 {
			return nil, fmt.Errorf("%s claims %s under %s, which runs %s",
				serving.owner, strings.Join(wide, ", "), serving.pointer, strings.Join(running[serving], " and "))
		}
	}
	table := &Table{
		grace:     grace,
		answering: map[string]answer{},
		connector: strings.ToLower(read.Connector),
		routed:    map[string]bool{},
	}
	answeredBy := map[string]route{}
	for _, appRoute := range standing {
		hostnames := claimed[appRoute.key()]
		if appRoute.app() && len(running[appRoute.surface()]) == 1 {
			hostnames = append(slices.Clone(hostnames), claimed[claimKey{appRoute.Owner, appRoute.Pointer, ""}]...)
		}
		slices.Sort(hostnames)
		for _, hostname := range hostnames {
			named := strings.ToLower(hostname)
			if first, answered := answeredBy[named]; answered {
				return nil, fmt.Errorf("%s would be forwarded by both %s and %s",
					hostname, first.identity(), appRoute.identity())
			}
			answeredBy[named] = appRoute
			table.answering[named] = answer{upstream: appRoute.Upstream, store: !appRoute.app()}
		}
		table.routed[appRoute.Upstream] = true
	}
	return table, nil
}

func (c claim) valid() error {
	switch {
	case c.Hostname == "" || c.Owner == "" || c.Pointer == "":
		return fmt.Errorf("a hostname claim is incomplete: host %q, surface %q, pointer %q",
			c.Hostname, c.Owner, c.Pointer)
	case strings.Contains(c.Hostname, "*"):
		return fmt.Errorf("a hostname claim names wildcard %q", c.Hostname)
	case strings.Contains(c.Owner, claimSeparator) || strings.Contains(c.Hostname, claimSeparator) ||
		strings.Contains(c.Pointer, claimSeparator) || strings.Contains(c.App, claimSeparator):
		return fmt.Errorf("the claim of %q by %q under pointer %q and app %q contains %q",
			c.Hostname, c.Owner, c.Pointer, c.App, claimSeparator)
	}
	return nil
}

func (r route) valid() (string, error) {
	for _, field := range []struct{ what, named string }{
		{"surface", r.Owner}, {"pointer", r.Pointer}, {"app", r.App},
	} {
		if field.named == "" || strings.Contains(field.named, claimSeparator) {
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

const (
	dnsLabelMax = 63
	dnsNameMax  = 253
)

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

func previewBaseUsable(base string) error {
	named := strings.ToLower(base)
	labels := strings.Split(named, ".")
	usable := len(labels) > 1 && len(named)+len(edge.LivenessProbeLabel)+1 <= dnsNameMax
	for _, label := range labels {
		usable = usable && len(label) <= dnsLabelMax && dnsLabel.MatchString(label)
	}
	if !usable {
		return fmt.Errorf("%q is not a usable preview base: want dotted dns labels of ≤%d bytes, with %s ≤%d bytes",
			base, dnsLabelMax, edge.ProbeHostname(edge.PreviewWildcard(base)), dnsNameMax)
	}
	return nil
}

func (t *Table) Forward(host, requested string) (Forward, bool) {
	named := hostOf(host)
	if t.connector != "" && named == t.connector && under(requested, ConnectorPath) {
		return Forward{Upstream: connectorDial, Strip: ConnectorPath}, true
	}
	answered, ok := t.answering[named]
	if !ok || answered.store && slices.ContainsFunc(storeAdminPaths, func(admin string) bool {
		return under(requested, admin) || under(path.Clean(requested), admin)
	}) {
		return Forward{}, false
	}
	return Forward{Upstream: answered.upstream}, true
}

func under(requested, prefix string) bool {
	lowered := strings.ToLower(requested)
	return lowered == prefix || strings.HasPrefix(lowered, prefix+"/")
}

func hostOf(host string) string {
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
