package cli

import (
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func edgeSelection(cfg *projectconfig.Config) *deploymentsv1.EdgeSelection {
	selection := &deploymentsv1.EdgeSelection{
		Kind:          string(cfg.EdgeKind()),
		AllowDegraded: cfg.AllowDegraded,
	}
	if cfg.DNS != nil {
		selection.Dns = &deploymentsv1.Dns{Kind: cfg.DNS.Kind, Zone: cfg.DNS.Zone}
	}
	return selection
}
