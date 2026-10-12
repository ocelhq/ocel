package providerserver_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
)

const cachedRoutes = `{"version":1,"basePath":"","trailingSlash":"ignore","headers":[],"redirects":[],` +
	`"routes":[{"src":"/blog/(.*)","function":"index","cache":{"revalidate":60}}],"fallback":{"function":"index"}}`

func ocelRoutesRoot(t *testing.T, hosting buildoutput.Hosting, routes string) string {
	t.Helper()
	hosting.RouteTable = edge.RouteTableOcel
	hosting.RootFunction = "index"
	root := servingRoot(t, "web", hosting, nil)
	if routes != "" {
		if err := os.WriteFile(filepath.Join(buildoutput.AppRoot(root, "web"), edge.OcelRoutesFile), []byte(routes), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestAnAppWithAnOcelRouteTableHandsItToItsOriginWhateverTheEdge(t *testing.T) {
	t.Parallel()

	root := ocelRoutesRoot(t, buildoutput.Hosting{Framework: buildoutput.FrameworkNode, Needs: revalidating}, cachedRoutes)
	for name, runsCode := range map[string]bool{"an edge that runs code": true, "an edge that runs none": false} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			query := servingQuery(root, "web", buildoutput.FrameworkNode)
			query.EdgeRunsCode = runsCode

			facts, err := providerserver.AppServingFor(query)
			if err != nil {
				t.Fatalf("AppServingFor() = %v", err)
			}
			routes := facts.OcelRoutes
			if routes == nil || routes.RootFunction != "index" || routes.RouteTable.Format != edge.RouteTableOcel || !bytes.Equal(routes.RouteTable.Table, []byte(cachedRoutes)) {
				t.Errorf("OcelRoutes = %+v, want the table the build wrote, bound to its root function", routes)
			}
			if facts.OriginDispatch != nil {
				t.Errorf("OriginDispatch = %+v, want none: an ocel table does not dispatch between functions", facts.OriginDispatch)
			}
			if located := facts.EdgeRouteTable != nil; located != runsCode {
				t.Errorf("EdgeRouteTable = %+v where the edge runs code = %v, want the table located exactly when the edge reads it", facts.EdgeRouteTable, runsCode)
			}
			if located := facts.EdgeRouteTable; located != nil && located.Location.Format != edge.RouteTableOcel {
				t.Errorf("EdgeRouteTable.Location.Format = %q, want %q", located.Location.Format, edge.RouteTableOcel)
			}
		})
	}
}

func TestAnAppWithAnOcelRouteTableAsksForTheRevalidationItsBuildDeclares(t *testing.T) {
	t.Parallel()

	root := ocelRoutesRoot(t, buildoutput.Hosting{Framework: buildoutput.FrameworkNode, Needs: revalidating}, cachedRoutes)
	facts, err := providerserver.AppServingFor(servingQuery(root, "web", buildoutput.FrameworkNode))
	if err != nil {
		t.Fatalf("AppServingFor() = %v", err)
	}
	if facts.ISR == nil || facts.ISR.Prefix == "" || facts.ISR.TagNamespace == "" {
		t.Errorf("ISR = %+v, want the ledger a revalidating node build writes tags through", facts.ISR)
	}
}

func TestAContainerAppIsNotHandedAnOcelRouteTable(t *testing.T) {
	t.Parallel()

	root := ocelRoutesRoot(t, buildoutput.Hosting{Framework: buildoutput.FrameworkNode, Needs: revalidating}, cachedRoutes)
	query := servingQuery(root, "web", buildoutput.FrameworkNode)
	query.Compute = provider.ComputeContainer

	facts, err := providerserver.AppServingFor(query)
	if err != nil {
		t.Fatalf("AppServingFor() = %v", err)
	}
	if facts.OcelRoutes != nil || facts.EdgeRouteTable != nil {
		t.Errorf("OcelRoutes = %+v, EdgeRouteTable = %+v, want neither: a container is one service with no function to bundle a table into", facts.OcelRoutes, facts.EdgeRouteTable)
	}
}

func TestAnOcelRouteTableTheBuildDidNotWriteOrNoEdgeCanReadIsRefused(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		hosting buildoutput.Hosting
		routes  string
		want    string
	}{
		"a build that wrote no table": {
			hosting: buildoutput.Hosting{Needs: revalidating},
			want:    edge.OcelRoutesFile,
		},
		"a table that is not valid": {
			hosting: buildoutput.Hosting{Needs: revalidating},
			routes:  strings.Replace(cachedRoutes, `"revalidate":60`, `"revalidate":0`, 1),
			want:    "revalidate",
		},
		"a cached route the build does not declare revalidation for": {
			routes: cachedRoutes,
			want:   string(edge.NeedRevalidation),
		},
		"a route to a function beside the root function": {
			hosting: buildoutput.Hosting{Needs: revalidating},
			routes:  strings.Replace(cachedRoutes, `"function":"index","cache"`, `"function":"api","cache"`, 1),
			want:    `"api"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tc.hosting.Framework = buildoutput.FrameworkNode
			root := ocelRoutesRoot(t, tc.hosting, tc.routes)

			_, err := providerserver.AppServingFor(servingQuery(root, "web", buildoutput.FrameworkNode))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("AppServingFor() = %v, want a refusal naming %s", err, tc.want)
			}
		})
	}
}

func TestAnOcelRouteTableWithNoCachedRouteNeedsNoRevalidation(t *testing.T) {
	t.Parallel()

	uncached := strings.Replace(cachedRoutes, `,"cache":{"revalidate":60}`, "", 1)
	root := ocelRoutesRoot(t, buildoutput.Hosting{Framework: buildoutput.FrameworkNode}, uncached)
	facts, err := providerserver.AppServingFor(servingQuery(root, "web", buildoutput.FrameworkNode))
	if err != nil {
		t.Fatalf("AppServingFor() = %v", err)
	}
	if facts.ISR != nil {
		t.Errorf("ISR = %+v, want no ledger for a build that declares no revalidation", facts.ISR)
	}
}
