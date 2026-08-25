package edgewire

import (
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func Selection(cfg *projectconfig.Config) *contractv1.EdgeSelection {
	selection := &contractv1.EdgeSelection{
		Kind:          string(cfg.EdgeKind()),
		AllowDegraded: cfg.AllowDegraded,
	}
	if cfg.DNS != nil {
		selection.Dns = &contractv1.Dns{Kind: cfg.DNS.Kind, Zone: cfg.DNS.Zone}
	}
	return selection
}
