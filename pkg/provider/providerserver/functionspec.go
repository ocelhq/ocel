package providerserver

import (
	"path/filepath"

	"github.com/ocelhq/ocel/pkg/edge"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

func functionSpecOf(fn *contractv1.ManifestFunction) provider.FunctionSpec {
	return provider.FunctionSpec{
		Name:           fn.GetLogicalName(),
		Route:          fn.GetRouteId(),
		EntryFile:      fn.GetEntryFile(),
		Framework:      frameworkOf(fn),
		HasWorkerEntry: fn.GetHasWorkerEntry(),
	}
}

func artifactDir(root string, fn *contractv1.ManifestFunction) string {
	if fn.GetArtifactPath() == "" {
		return ""
	}
	return filepath.Join(root, filepath.FromSlash(fn.GetArtifactPath()))
}

func overlayFor(fn *contractv1.ManifestFunction, routing *provider.RoutingSpec) map[string][]byte {
	if routing == nil || routing.RouteTable.Format == edge.RouteTableNext && resolveRouteID(fn) != routing.RootFunction {
		return nil
	}
	return map[string][]byte{routeTableFiles[routing.RouteTable.Format]: routing.RouteTable.Table}
}

func resolveRouteID(fn *contractv1.ManifestFunction) string {
	if route := fn.GetRouteId(); route != "" {
		return route
	}
	return fn.GetLogicalName()
}
