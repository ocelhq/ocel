package deploy

import (
	"context"
	"fmt"
	"strconv"

	"github.com/ocelhq/ocel/pkg/naming"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/vars"
	"github.com/ocelhq/ocel/platform/aws/provider/vars/live"
)

type LinkPublisher interface {
	PublishLinks(ctx context.Context, slug, environment string, links []vars.LinkValue) (int, error)
}

func manifestLinks(manifest *deploymentsv1.Manifest) []live.Link {
	resources := manifest.GetResources()
	out := make([]live.Link, 0, len(resources))
	for _, r := range resources {
		out = append(out, live.Link{
			Name: r.GetLogicalName(),
			Key:  functionEnvKey(r.GetResource().GetType(), r.GetResource().GetName()),
		})
	}
	return out
}

func linkValues(manifest *deploymentsv1.Manifest, links []*linksv1.Link) ([]vars.LinkValue, error) {
	byName := make(map[string]*linksv1.Link, len(links))
	for _, l := range links {
		byName[l.GetName()] = l
	}

	out := make([]vars.LinkValue, 0, len(links))
	for _, r := range manifest.GetResources() {
		link, ok := byName[r.GetLogicalName()]
		if !ok {
			continue
		}
		value, err := linkPayload(link)
		if err != nil {
			return nil, err
		}
		out = append(out, vars.LinkValue{
			Link:  r.GetLogicalName(),
			Key:   functionEnvKey(link.GetType(), r.GetResource().GetName()),
			Value: value,
		})
	}
	return out, nil
}

func linkPayload(link *linksv1.Link) (string, error) {
	p := link.GetProperties()
	switch link.GetType() {
	case naming.TokenPostgres:
		port, err := strconv.Atoi(p["port"])
		if err != nil {
			return "", fmt.Errorf("link %s reports port %q, which is not a number", link.GetName(), p["port"])
		}
		return postgresEnvPayload(p["username"], p["password"], p["host"], port, p["database"]), nil
	case naming.TokenBucket:
		return bucketEnvPayload(p["bucket"]), nil
	}
	return "", fmt.Errorf("link %s is a %s, a type this provider ships no client for", link.GetName(), link.GetType())
}

func publishLinkValues(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest, links []*linksv1.Link) error {
	values, err := linkValues(manifest, links)
	if err != nil {
		return err
	}
	if cfg.Links == nil {
		if len(values) == 0 {
			return nil
		}
		return fmt.Errorf("this deploy provisions %d linked resources but reached no variable store to deliver their values through", len(values))
	}
	if _, err := cfg.Links.PublishLinks(ctx, manifest.GetSlug(), overrideEnvironment(cfg), values); err != nil {
		return fmt.Errorf("deliver link values: %w", err)
	}
	return nil
}
