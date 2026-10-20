package names

import "github.com/ocelhq/ocel/pkg/environment"

const TierTagKey = "ocel-tier"

func (n Names) TierTagKeyPath() string { return n.project + "/" + TierTagKey }

func (n Names) TierTagValuePath(tier environment.Tier) string {
	return n.TierTagKeyPath() + "/" + string(tier)
}
