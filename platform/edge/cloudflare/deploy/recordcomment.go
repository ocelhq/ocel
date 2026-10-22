package cloudflare

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

const (
	maxRecordCommentLength = 100
	recordCommentHead      = "managed by ocel"
	recordOriginMarker     = " on "
	recordOwnerMarker      = " for "
)

type recordComment struct {
	origin OriginScope
	owner  string
}

func formatWriterComment(origin OriginScope) string {
	return recordCommentHead + recordOriginMarker + origin.String()
}

func formatForwardingOwner(namespace string, origin OriginScope, slug string, tier environment.Tier) (string, error) {
	if err := refuseUnknownTier("forwarded records", tier); err != nil {
		return "", err
	}
	if err := origin.refuseUnrecordable(); err != nil {
		return "", err
	}
	written := recordOriginMarker + origin.String()
	project, err := naming.TaggedName{
		Head:     naming.OcelName(namespace, tier),
		Readable: naming.Sanitize(slug),
		Tag:      naming.HashTag(namespace, slug, string(tier)),
	}.Fit(maxRecordCommentLength - len(recordCommentHead+recordOwnerMarker) - len(written))
	if err != nil {
		return "", err
	}
	return project + written, nil
}

func formatPreviewForwardingOwner(origin OriginScope) string {
	return edge.PreviewEntryOwner + recordOriginMarker + origin.String()
}

func formatOwnerComment(owner string) string { return recordCommentHead + recordOwnerMarker + owner }

func parseRecordComment(comment string) (recordComment, bool) {
	if written, isWriter := strings.CutPrefix(comment, recordCommentHead+recordOriginMarker); isWriter {
		origin, readable := parseOriginScope(written)
		return recordComment{origin: origin}, readable
	}
	owner, forwarded := strings.CutPrefix(comment, recordCommentHead+recordOwnerMarker)
	if !forwarded {
		return recordComment{}, false
	}
	held, written, named := strings.Cut(owner, recordOriginMarker)
	if !named || held == "" {
		return recordComment{}, false
	}
	origin, readable := parseOriginScope(written)
	return recordComment{origin: origin, owner: owner}, readable
}
