package names

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

const (
	TierTagKey   = "ocel-tier"
	OutsideValue = "outside"
)

func (n Names) TierTagKeyPath() string { return n.project + "/" + TierTagKey }

func (n Names) TierTagValuePath(tier environment.Tier) string {
	return n.TierTagKeyPath() + "/" + string(tier)
}

func (n Names) OutsideTagValuePath() string { return n.TierTagKeyPath() + "/" + OutsideValue }

func (n Names) IsTierAccount(id string) bool {
	for _, prefix := range []string{
		n.AppFence(),
		naming.OcelName(string(n.namespace), environment.TierProduction) + naming.WordSeparator,
		naming.OcelName(string(n.namespace), environment.TierPreview) + naming.WordSeparator,
	} {
		if strings.HasPrefix(id, prefix) {
			return true
		}
	}
	return false
}
