package provider_test

import (
	"maps"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
)

func TestARoutedAppHandsItsDispatcherTheRouteTableBuildAndStaticRules(t *testing.T) {
	app := &provider.AppSpec{
		BuildID: "b1",
		Routing: &provider.RoutingSpec{RootFunction: "server"},
		Static:  &edge.Static{ImmutablePrefixes: []string{"/_next/static/"}},
	}

	want := map[string]string{
		"OCEL_NEXT_ROUTE_TABLE": "/ocel/app/next-route-table.json",
		"OCEL_BUILD_ID":         "b1",
		"OCEL_STATIC_RULES":     `{"immutablePrefixes":["/_next/static/"]}`,
	}
	if got := provider.NewRouteTableEnv(app, "/ocel/app/next-route-table.json"); !maps.Equal(got, want) {
		t.Errorf("NewRouteTableEnv() = %v, want %v", got, want)
	}
}

func TestAnAppWithNoRoutingHandsNoDispatcherAnything(t *testing.T) {
	if got := provider.NewRouteTableEnv(&provider.AppSpec{BuildID: "b1"}, "/ocel/app/next-route-table.json"); len(got) != 0 {
		t.Errorf("NewRouteTableEnv() = %v, want nothing: no dispatcher runs", got)
	}
}

func TestAnOriginThatDispatchesSaysSoAndThatItsForwardsAreSigned(t *testing.T) {
	want := map[string]string{"OCEL_ORIGIN_DISPATCH": "1", "OCEL_ORIGIN_SIGNED": "1"}
	if got := provider.NewOriginDispatchEnv(); !maps.Equal(got, want) {
		t.Errorf("NewOriginDispatchEnv() = %v, want %v", got, want)
	}
}
