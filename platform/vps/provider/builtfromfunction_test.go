package vps_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/router"
)

func nextAppBuiltFromItsFunction(t *testing.T) provider.StackSpec {
	t.Helper()
	app := anApp()
	app.Framework = buildoutput.FrameworkNext
	app.Router = "switchboard"
	app.Functions = []provider.FunctionSpec{{Name: "server", Route: "server", Framework: buildoutput.Framework{Name: buildoutput.FrameworkNext}}}
	app.Routing = &provider.RoutingSpec{RootFunction: "server", RouteTable: router.RouteTable{Format: edge.RouteTableNext, Table: []byte(`{"rootFunction":"server"}`)}}
	app.ISR = &provider.ISRSpec{TagNamespace: "PROJECT#shop#STACK#prod--web--r0a1b2c3d#TAG#"}
	app.Static = &edge.Static{ImmutablePrefixes: []string{"/_next/static/"}}
	spec := aStack(t, app)
	spec.App.StorageCoordinate = naming.Coordinate{Project: "shop", Env: "prod", App: "web", Release: spec.Ref.Name.Release}
	return spec
}

func TestANextContainerBuiltFromItsFunctionServesItsPathsOffItsOwnDisk(t *testing.T) {
	t.Parallel()

	machine := &box{}
	started, err := over(machine).ProvisionContainers(context.Background(), nextAppBuiltFromItsFunction(t), progress.Discard())
	if err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}

	file := envFileWritten(t, machine, environment.TierProduction, started[0].Physical)
	for _, want := range []string{
		edge.OriginDispatchVar + "=1",
		edge.OriginSignedVar + "=1",
		"OCEL_NEXT_ROUTE_TABLE=/ocel/app/" + edge.NextRouteTableFile,
		"OCEL_BUILD_ID=" + buildID,
		"OCEL_ROUTER_KIND=switchboard",
		"OCEL_STORAGE_COORDINATE=shop/prod/web/r0a1b2c3d",
	} {
		if !strings.Contains(file, want) {
			t.Errorf("the env file reads %q, want %s: the Next runtime in the function's image dispatches its paths off the box", file, want)
		}
	}
	for _, unwanted := range []string{"OCEL_EDGE_BUCKET", "OCEL_CACHE_BUCKET", "OCEL_TAG_DATABASE", "OCEL_ISR="} {
		if strings.Contains(file, unwanted) {
			t.Errorf("the env file reads %q, want no %s: a box has no bucket and no tag database, and Next keeps its own cache in the container", file, unwanted)
		}
	}
}
