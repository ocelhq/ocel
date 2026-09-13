package costkit

import (
	"maps"
	"slices"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Item struct {
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

type EdgeInventory struct {
	Vendor      string
	Region      string
	BillsEgress bool
	Shared      []Item
	Environment []Item
	Apps        map[string][]Item
}

type EdgeInventorier interface {
	CostInventory(site EdgeSite) (EdgeInventory, error)
}

type EdgePricer interface {
	CostCard() (*Card, error)
	CostTable() Table
}

func InventoryEdge(front edge.Edge, site EdgeSite) (EdgeInventory, error) {
	inventorier, takes := front.(EdgeInventorier)
	if !takes {
		return EdgeInventory{}, nil
	}
	return inventorier.CostInventory(site)
}

type EdgeScopes struct {
	Shared      string
	Environment string
}

func (t *Tree) AddEdge(scopes EdgeScopes, inventory EdgeInventory) {
	t.AddItems(scopes.Shared, inventory.Vendor, inventory.Region, inventory.Shared)
	t.AddItems(scopes.Environment, inventory.Vendor, inventory.Region, inventory.Environment)
	for _, app := range slices.Sorted(maps.Keys(inventory.Apps)) {
		t.AddItems(t.Scope(scopes.Environment, ScopeApp, app), inventory.Vendor, inventory.Region, inventory.Apps[app])
	}
}

func Priced(card *Card, table Table, edges ...EdgePricer) (*Card, Table, error) {
	cards := []*Card{}
	tables := []Table{table}
	for _, pricer := range edges {
		held, err := pricer.CostCard()
		if err != nil {
			return nil, nil, err
		}
		cards = append(cards, held)
		tables = append(tables, pricer.CostTable())
	}
	return Merge(card, cards...), Tables(tables...), nil
}
