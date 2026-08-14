package cli

import (
	"fmt"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/servicemap"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
)

func publishServiceMap(cfg *projectconfig.Config, manifest *deploymentsv1.Manifest, env *deploymentsv1.Environment, tag, promotionID string, links []*linksv1.Link) error {
	record := servicemap.Derive(servicemap.Deploy{
		Slug: cfg.Slug,
		Environment: servicemap.Environment{
			Class:    environmentClassKey(env.GetClass()),
			Identity: env.GetIdentity(),
		},
		PromotionID: promotionID,
		Tag:         tag,
	}, manifest, links)

	if err := servicemap.Publish(cfg.Dir, record); err != nil {
		return fmt.Errorf("publish service map: %w", err)
	}
	return nil
}
