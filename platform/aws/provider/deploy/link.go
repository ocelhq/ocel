package deploy

import (
	"context"

	"github.com/ocelhq/ocel/pkg/naming"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
)

type LinkStore interface {
	PublishRecords(ctx context.Context, slug, environment, owner string, records []*linksv1.Link) error
	ResolveRecords(ctx context.Context, slug, environment string, names []string) ([]PublishedRecord, error)
	PublishedNames(ctx context.Context, slug, class, environment string) ([]string, error)
}

type PublishedRecord struct {
	Link    *linksv1.Link
	Version int64
}

func (r PublishedRecord) Name() string { return r.Link.GetName() }

func (r PublishedRecord) Type() linksv1.LinkType { return naming.LinkTypeOf(r.Link) }
