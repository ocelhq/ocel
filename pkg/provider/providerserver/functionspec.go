package providerserver

import (
	"os"
	"path/filepath"

	"github.com/ocelhq/ocel/pkg/edge"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
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

func artifactDir(root string, fn *contractv1.ManifestFunction) (string, error) {
	rel := filepath.FromSlash(fn.GetArtifactPath())
	if rel == "" {
		return "", nil
	}
	if !filepath.IsLocal(rel) {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"function %s's build path %q leaves the build root: run `ocel build` again", fn.GetLogicalName(), fn.GetArtifactPath())
	}
	dir := filepath.Join(root, rel)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"function %s has no build at %s: run `ocel build` again", fn.GetLogicalName(), dir)
	}
	return dir, nil
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
