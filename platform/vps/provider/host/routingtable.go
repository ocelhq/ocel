package host

import (
	"bytes"
	"encoding/json"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

type RoutingTable struct {
	Grace       time.Duration
	Claims      []HostClaim
	Routes      []AppRoute
	Pins        []Pin
	Shields     []Shield
	PreviewBase string
	Connector   string
	Tunneled    []TunneledHost
	Tunnel      *Tunnel
	Retired     []Tunnel
}

type tableRows struct {
	Grace       string         `json:"grace"`
	Claims      []HostClaim    `json:"claims,omitempty"`
	Routes      []AppRoute     `json:"routes,omitempty"`
	Pins        []Pin          `json:"pins,omitempty"`
	Shields     []Shield       `json:"shields,omitempty"`
	PreviewBase string         `json:"preview,omitempty"`
	Connector   string         `json:"connector,omitempty"`
	Tunneled    []TunneledHost `json:"tunneled,omitempty"`
	Tunnel      *Tunnel        `json:"tunnel,omitempty"`
	Retired     []Tunnel       `json:"retired,omitempty"`
}

func WriteRoutingTable(table RoutingTable) ([]byte, error) {
	return json.Marshal(tableRows{
		Grace:       spelled(table.Grace),
		Claims:      slices.SortedFunc(slices.Values(table.Claims), byClaimed),
		Routes:      slices.SortedFunc(slices.Values(table.Routes), byKey),
		Pins:        slices.SortedFunc(slices.Values(table.Pins), byPinned),
		Shields:     slices.SortedFunc(slices.Values(table.Shields), byHostnameThenOwner),
		PreviewBase: table.PreviewBase,
		Connector:   table.Connector,
		Tunneled:    slices.SortedFunc(slices.Values(table.Tunneled), byHostnameThenOwner),
		Tunnel:      table.Tunnel,
		Retired:     table.Retired,
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
		Shields:     rows.Shields,
		PreviewBase: rows.PreviewBase,
		Connector:   rows.Connector,
		Tunneled:    rows.Tunneled,
		Tunnel:      rows.Tunnel,
		Retired:     rows.Retired,
	}
	if err := validTable(table); err != nil {
		return RoutingTable{}, unrenderable(err)
	}
	return table, nil
}

func unrenderable(err error) error {
	return refusal.Refuse(refusal.CodeInvalid,
		"%s contains what ocel cannot render into %s: %v",
		live.RoutingTable, ProxyConfig, err)
}
