package cli

import (
	"fmt"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/servicemap"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func publishServiceMap(cfg *projectconfig.Config, manifest *contractv1.Manifest, env *environmentv1.Environment, tag, promotionID string, links []*linksv1.Link) error {
	record := servicemap.Derive(servicemap.Deploy{
		Slug: cfg.Slug,
		Environment: servicemap.Environment{
			Class:    environmentClassKey(env.GetTier()),
			Identity: env.GetIdentity(),
		},
		PromotionID: promotionID,
		Tag:         tag,
	}, manifest, links)

	if err := servicemap.Write(cfg.Dir, record); err != nil {
		return fmt.Errorf("publish service map: %w", err)
	}
	return nil
}
