package commands

import (
	"errors"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

func ChooseTier(preview bool) environmentv1.Tier {
	if preview {
		return environmentv1.Tier_TIER_PREVIEW
	}
	return environmentv1.Tier_TIER_PRODUCTION
}

func RefuseEnvironmentWithoutPreview(preview bool, environment string) error {
	if environment == "" || preview {
		return nil
	}
	return errors.New("--environment addresses one preview environment's override, and production has a single environment; pass --preview, or leave --environment off to address the production value")
}
