package providerserver

import (
	"errors"
	"fmt"
	"strings"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/environment"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

func decodeTier(tier environmentv1.Tier) (environment.Tier, error) {
	switch tier {
	case environmentv1.Tier_TIER_PRODUCTION, environmentv1.Tier_TIER_UNSPECIFIED:
		return environment.TierProduction, nil
	case environmentv1.Tier_TIER_PREVIEW:
		return environment.TierPreview, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"there is no %s bootstrap; a bootstrap is either production or preview",
			strings.ToLower(strings.TrimPrefix(tier.String(), "TIER_"))))
	}
}

func decodeRequiredTier(tier environmentv1.Tier) (environment.Tier, error) {
	if tier == environmentv1.Tier_TIER_UNSPECIFIED {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New(
			"this request is answered for the production or preview tier, and it named neither"))
	}
	return decodeTier(tier)
}

func encodeTier(tier environment.Tier) environmentv1.Tier {
	if tier == environment.TierPreview {
		return environmentv1.Tier_TIER_PREVIEW
	}
	return environmentv1.Tier_TIER_PRODUCTION
}
