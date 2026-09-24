package host

import (
	"bytes"
	"cmp"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

type RoutingTable struct {
	Grace       time.Duration
	Claims      []HostClaim
	Routes      []AppRoute
	Pins        []Pin
	PreviewBase string
	Connector   string
}

type tableRows struct {
	Grace     string      `json:"grace"`
	Claims    []HostClaim `json:"claims,omitempty"`
	Routes    []AppRoute  `json:"routes,omitempty"`
	Pins      []Pin       `json:"pins,omitempty"`
	Preview   string      `json:"preview,omitempty"`
	Connector string      `json:"connector,omitempty"`
}

func WriteRoutingTable(table RoutingTable) ([]byte, error) {
	return json.Marshal(tableRows{
		Grace: spelled(table.Grace),
		Claims: slices.SortedFunc(slices.Values(table.Claims), func(a, b HostClaim) int {
			return strings.Compare(a.Hostname, b.Hostname)
		}),
		Routes: slices.SortedFunc(slices.Values(table.Routes), byKey),
		Pins: slices.SortedFunc(slices.Values(table.Pins), func(a, b Pin) int {
			return cmp.Or(strings.Compare(a.Hostname, b.Hostname), strings.Compare(a.Path, b.Path))
		}),
		Preview:   table.PreviewBase,
		Connector: table.Connector,
	})
}

func ReadRoutingTable(document []byte) (RoutingTable, error) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var rows tableRows
	if err := decoder.Decode(&rows); err != nil {
		return RoutingTable{}, unrenderable(err)
	}
	grace, err := time.ParseDuration(rows.Grace)
	if err != nil {
		return RoutingTable{}, unrenderable(err)
	}
	table := RoutingTable{
		Grace:       grace,
		Claims:      rows.Claims,
		Routes:      rows.Routes,
		Pins:        rows.Pins,
		PreviewBase: rows.Preview,
		Connector:   rows.Connector,
	}
	if _, err := RenderProxyConfig(table); err != nil {
		return RoutingTable{}, unrenderable(err)
	}
	return table, nil
}

func unrenderable(err error) error {
	return providerkit.Refuse(providerkit.CodeInvalid,
		"%s holds what ocel cannot render into %s: %v",
		live.RoutingTable, ProxyConfig, err)
}
