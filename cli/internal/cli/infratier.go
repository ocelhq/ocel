package cli

import (
	"fmt"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/environment/v1"
)

func checkTier(infra, required environmentv1.Tier) error {
	if infra == required {
		return nil
	}

	switch required {
	case environmentv1.Tier_TIER_PREVIEW:
		return fmt.Errorf(
			"ocel preview can only run against preview infrastructure, but the account points at %s; run `ocel bootstrap --preview` to stand up preview infrastructure",
			infraLabel(infra),
		)
	case environmentv1.Tier_TIER_PRODUCTION:
		return fmt.Errorf(
			"ocel deploy can only run against production infrastructure, but the account points at %s; run `ocel bootstrap` to stand up production infrastructure",
			infraLabel(infra),
		)
	default:
		return fmt.Errorf(
			"the account points at %s, but this command requires %s",
			infraLabel(infra), infraLabel(required),
		)
	}
}

func infraLabel(tier environmentv1.Tier) string {
	switch tier {
	case environmentv1.Tier_TIER_PREVIEW:
		return "preview infrastructure"
	case environmentv1.Tier_TIER_PRODUCTION:
		return "production infrastructure"
	default:
		return "no Ocel infrastructure"
	}
}
