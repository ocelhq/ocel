package pricing

import (
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
)

type Item struct {
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
	Shared      []Item
	Environment []Item
	Apps        map[string][]Item
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
