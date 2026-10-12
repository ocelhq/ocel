package edge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type routesFixture struct {
	Table    json.RawMessage `json:"table"`
	Patterns []struct {
		Src    string    `json:"src"`
		Path   string    `json:"path"`
		Groups *[]string `json:"groups"`
	} `json:"patterns"`
	Invalid []struct {
		Reason string          `json:"reason"`
		Table  json.RawMessage `json:"table"`
	} `json:"invalid"`
}

func readRoutesFixture(t *testing.T) routesFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "platform", "edge", "contract", "fixtures", "ocel-route-table.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture routesFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestTheFixturesRouteTableIsAccepted(t *testing.T) {
	t.Parallel()

	routes, err := ReadOcelRouteTable(readRoutesFixture(t).Table)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes.Routes) != 4 || routes.Routes[1].Cache == nil || routes.Routes[1].Cache.Expire != 600 {
		t.Errorf("routes = %+v, want the four routes of the fixture with their cache rules", routes.Routes)
	}
}

func TestEveryInvalidRouteTableOfTheFixtureIsRefused(t *testing.T) {
	t.Parallel()

	for _, c := range readRoutesFixture(t).Invalid {
		if _, err := ReadOcelRouteTable(c.Table); err == nil {
			t.Errorf("a table with %s was accepted: %s", c.Reason, c.Table)
		}
	}
}

func TestAPatternMatchesTheFixturePathsTheWayTheEdgeContractDoes(t *testing.T) {
	t.Parallel()

	for _, c := range readRoutesFixture(t).Patterns {
		compiled, err := compilePattern(c.Src)
		if err != nil {
			t.Errorf("%s was refused: %v", c.Src, err)
			continue
		}
		groups := compiled.FindStringSubmatch(c.Path)
		switch {
		case c.Groups == nil && groups != nil:
			t.Errorf("%s matched %s, want no match", c.Src, c.Path)
		case c.Groups != nil && groups == nil:
			t.Errorf("%s did not match %s, want groups %q", c.Src, c.Path, *c.Groups)
		case c.Groups != nil && !slices.Equal(groups[1:], *c.Groups):
			t.Errorf("%s matched %s with groups %q, want %q", c.Src, c.Path, groups[1:], *c.Groups)
		}
	}
}

func TestATableWithACachedRouteSaysSo(t *testing.T) {
	t.Parallel()

	routes, err := ReadOcelRouteTable(readRoutesFixture(t).Table)
	if err != nil {
		t.Fatal(err)
	}
	if !routes.HasCachedRoute() {
		t.Error("HasCachedRoute = false for a table whose first route is cached")
	}
	routes.Routes = routes.Routes[2:]
	if routes.HasCachedRoute() {
		t.Error("HasCachedRoute = true for a table whose routes are none of them cached")
	}
}

func TestATableNamesNoFunctionBesideTheOnesItsBuildServes(t *testing.T) {
	t.Parallel()

	routes, err := ReadOcelRouteTable(readRoutesFixture(t).Table)
	if err != nil {
		t.Fatal(err)
	}
	if got := routes.Functions(); !slices.Equal(got, []string{"index"}) {
		t.Errorf("Functions = %q, want index once", got)
	}
	routes.Routes[3].Function = "api"
	if got := routes.Functions(); !slices.Equal(got, []string{"api", "index"}) {
		t.Errorf("Functions = %q, want api and index sorted", got)
	}
}

func TestATableRefusesATrailingGarbageDocument(t *testing.T) {
	t.Parallel()

	table := strings.TrimSpace(string(readRoutesFixture(t).Table)) + ` {}`
	if _, err := ReadOcelRouteTable([]byte(table)); err == nil {
		t.Error("a table followed by a second document was accepted")
	}
}
