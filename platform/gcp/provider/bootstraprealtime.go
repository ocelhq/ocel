package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
)

func realtimePurpose(tier environment.Tier) accountPurpose {
	return accountPurpose{
		displayName: "ocel " + string(tier) + " realtime",
		description: "the identity the realtime gateways of the " + string(tier) + " tier run as, which may read each environment's realtime keys and nothing else",
		ungranted:   "it is granted nothing in the project, so nothing is missing",
		grant:       func(context.Context, environment.Tier) error { return nil },
		forget:      func(context.Context, environment.Tier) error { return nil },
		granted:     func(context.Context, environment.Tier) (bool, error) { return true, nil },
	}
}
