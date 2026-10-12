package providerserver

import (
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
