package costkit

import (
	"maps"
	"slices"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Shaped struct {
	Name       string
	Type       string
	Properties map[string]any
}

type EdgeSite struct {
	Slug   string
	Class  edge.Class
	Region string
	Apps   []EdgeApp
}

type EdgeApp struct {
	Name      string
	Hostnames []string
}

type EdgeShape struct {
	Vendor      string
	Region      string
	BillsEgress bool
	Shared      []Shaped
	Environment []Shaped
	Apps        map[string][]Shaped
}

type EdgeRates struct {
	Card  func() (*Card, error)
	Table Table
}

type EdgeScopes struct {
	Shared      string
	Environment string
}

func (t *Tree) AddEdge(scopes EdgeScopes, shape EdgeShape) {
	t.AddShaped(scopes.Shared, shape.Vendor, shape.Region, shape.Shared)
	t.AddShaped(scopes.Environment, shape.Vendor, shape.Region, shape.Environment)
	for _, app := range slices.Sorted(maps.Keys(shape.Apps)) {
		t.AddShaped(t.Scope(scopes.Environment, ScopeApp, app), shape.Vendor, shape.Region, shape.Apps[app])
	}
}

func Priced(card *Card, table Table, edges ...EdgeRates) (*Card, Table, error) {
	cards := []*Card{}
	tables := []Table{table}
	for _, rates := range edges {
		held, err := rates.Card()
		if err != nil {
			return nil, nil, err
		}
		cards = append(cards, held)
		tables = append(tables, rates.Table)
	}
	return Merge(card, cards...), Tables(tables...), nil
}
