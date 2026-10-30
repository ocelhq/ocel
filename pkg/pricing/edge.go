package pricing

import (
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
)

type Resource struct {
	Name       string
	Type       string
	Properties map[string]any
}

type EdgeSite struct {
	Slug   string
	Tier   environment.Tier
	Region string
	Apps   []EdgeApp
}

type EdgeApp struct {
	Name      string
	Hostnames []string
}

type EdgeInventory struct {
	Vendor      string
	Region      string
	BillsEgress bool
	Shared      []Resource
	Environment []Resource
	Apps        map[string][]Resource
}

type EdgeRates struct {
	Card  func() (*Card, error)
	Table Table
}

type EdgeScopes struct {
	Shared      string
	Environment string
}

func (t *Tree) AddEdge(scopes EdgeScopes, inventory EdgeInventory) {
	t.AddResources(scopes.Shared, inventory.Vendor, inventory.Region, inventory.Shared)
	t.AddResources(scopes.Environment, inventory.Vendor, inventory.Region, inventory.Environment)
	for _, app := range slices.Sorted(maps.Keys(inventory.Apps)) {
		t.AddResources(t.Scope(scopes.Environment, ScopeApp, app), inventory.Vendor, inventory.Region, inventory.Apps[app])
	}
}

func Priced(card *Card, table Table, edges ...EdgeRates) (*Card, Table, error) {
	cards := []*Card{}
	tables := []Table{table}
	for _, rates := range edges {
		card, err := rates.Card()
		if err != nil {
			return nil, nil, err
		}
		cards = append(cards, card)
		tables = append(tables, rates.Table)
	}
	return Merge(card, cards...), Tables(tables...), nil
}
