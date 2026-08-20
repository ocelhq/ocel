package deploy

import (
	"context"
	"fmt"

	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/vars"
	"github.com/ocelhq/ocel/platform/aws/provider/vars/live"
)

type LinkStore interface {
	PublishRecords(ctx context.Context, slug, environment, owner string, records []*linksv1.Link) (vars.PublishResult, error)
	ResolveRecords(ctx context.Context, slug, environment string, names []string) ([]vars.PublishedRecord, error)
	PublishedNames(ctx context.Context, slug, class, environment string) ([]string, error)
}

func linkName(r *contractv1.ManifestResource) string {
	if r.GetLinked() {
		return r.GetResource().GetName()
	}
	return r.GetLogicalName()
}

func appLinks(manifest *contractv1.Manifest, app string, consumed map[string]Consumed) []live.Link {
	used := usedResources(manifest, app)
	resources := manifest.GetResources()
	out := make([]live.Link, 0, len(resources))
	for _, r := range resources {
		if !used[r.GetLogicalName()] {
			continue
		}
		record := live.Link{
			Name: linkName(r),
			Key:  functionEnvKey(r.GetResource().GetType(), r.GetResource().GetName()),
			Type: r.GetResource().GetType(),
		}
		if r.GetLinked() {
			record.Granted = consumed[r.GetLogicalName()].Record.Version
		}
		out = append(out, record)
	}
	return out
}

func linkRecords(manifest *contractv1.Manifest, links []*linksv1.Link) []*linksv1.Link {
	byName := make(map[string]*linksv1.Link, len(links))
	for _, l := range links {
		byName[l.GetName()] = l
	}

	out := make([]*linksv1.Link, 0, len(links))
	for _, r := range manifest.GetResources() {
		link, ok := byName[r.GetLogicalName()]
		if !ok || r.GetLinked() {
			continue
		}
		out = append(out, link)
	}
	return out
}

func publishLinkRecords(ctx context.Context, cfg Config, manifest *contractv1.Manifest, links []*linksv1.Link) error {
	records := linkRecords(manifest, links)
	if cfg.Links == nil {
		if len(records) == 0 {
			return nil
		}
		return fmt.Errorf("this deploy provisions %d linked resources but reached no variable store to deliver their values through", len(records))
	}
	if _, err := cfg.Links.PublishRecords(ctx, manifest.GetSlug(), overrideEnvironment(cfg), vars.OwnerOcel, records); err != nil {
		return fmt.Errorf("deliver link values: %w", err)
	}
	return nil
}
