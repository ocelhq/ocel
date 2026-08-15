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
	PublishRecords(ctx context.Context, slug, environment string, records []vars.Record) (int, error)
}

var linkRecordShape = map[string][]string{
	naming.TokenPostgres: {"connectionString"},
	naming.TokenBucket:   {"bucket"},
}

func manifestLinks(manifest *deploymentsv1.Manifest) []live.Link {
	resources := manifest.GetResources()
	out := make([]live.Link, 0, len(resources))
	for _, r := range resources {
		token := r.GetResource().GetType()
		out = append(out, live.Link{
			Name:       r.GetLogicalName(),
			Key:        functionEnvKey(token, r.GetResource().GetName()),
			Type:       token,
			Properties: linkRecordShape[token],
		})
	}
	return out
}

func linkRecords(manifest *deploymentsv1.Manifest, links []*linksv1.Link) ([]vars.Record, error) {
	byName := make(map[string]*linksv1.Link, len(links))
	for _, l := range links {
		byName[l.GetName()] = l
	}

	out := make([]vars.Record, 0, len(links))
	for _, r := range manifest.GetResources() {
		link, ok := byName[r.GetLogicalName()]
		if !ok {
			continue
		}
		properties, err := linkProperties(link)
		if err != nil {
			return nil, err
		}
		out = append(out, vars.Record{
			Name:       r.GetLogicalName(),
			Type:       link.GetType(),
			Properties: properties,
			Grants:     linkGrants(link),
		})
	}
	return out, nil
}

func linkGrants(link *linksv1.Link) []vars.Grant {
	granted := link.GetGrants()
	if len(granted) == 0 {
		return nil
	}
	out := make([]vars.Grant, 0, len(granted))
	for _, g := range granted {
		out = append(out, vars.Grant{Actions: g.GetActions(), Resources: g.GetResources(), Label: g.GetLabel()})
	}
	return out
}

func linkProperties(link *linksv1.Link) (map[string]string, error) {
	p := link.GetProperties()
	switch link.GetType() {
	case naming.TokenPostgres:
		port, err := strconv.Atoi(p["port"])
		if err != nil {
			return nil, fmt.Errorf("link %s reports port %q, which is not a number", link.GetName(), p["port"])
		}
		return postgresRecordProperties(p["username"], p["password"], p["host"], port, p["database"]), nil
	case naming.TokenBucket:
		return bucketRecordProperties(p["bucket"]), nil
	}
	return nil, fmt.Errorf("link %s is a %s, a type this provider ships no client for", link.GetName(), link.GetType())
}

func publishLinkRecords(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest, links []*linksv1.Link) error {
	records, err := linkRecords(manifest, links)
	if err != nil {
		return err
	}
	if cfg.Links == nil {
		if len(records) == 0 {
			return nil
		}
		return fmt.Errorf("this deploy provisions %d linked resources but reached no variable store to deliver their values through", len(records))
	}
	if _, err := cfg.Links.PublishRecords(ctx, manifest.GetSlug(), overrideEnvironment(cfg), records); err != nil {
		return fmt.Errorf("deliver link values: %w", err)
	}
	return nil
}
