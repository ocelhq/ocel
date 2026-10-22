package cloudflare

import (
	"strings"

	"github.com/cloudflare/cloudflare-go/v4/dns"

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

type recordOwner struct {
	origin       OriginScope
	forwardedFor string
}

func (n originNames) forwardingOwner(slug string, tier environment.Tier) (recordOwner, error) {
	if err := refuseUnknownTier("forwarded records", tier); err != nil {
		return recordOwner{}, err
	}
	written := recordOriginMarker + n.origin.String()
	forwardedFor, err := naming.TaggedName{
		Head:     naming.OcelName(n.namespace, tier),
		Readable: naming.Sanitize(slug),
		Tag:      naming.HashTag(n.namespace, slug, string(tier)),
	}.Fit(maxRecordCommentLength - len(recordCommentHead+recordOwnerMarker) - len(written))
	if err != nil {
		return recordOwner{}, err
	}
	return recordOwner{origin: n.origin, forwardedFor: forwardedFor}, nil
}

func dnsWriter(origin OriginScope) recordOwner { return recordOwner{origin: origin} }

func previewForwardingOwner(origin OriginScope) recordOwner {
	return recordOwner{origin: origin, forwardedFor: edge.PreviewEntryOwner}
}

func (o recordOwner) isForwarding() bool { return o.forwardedFor != "" }

func (o recordOwner) isOtherOrigin(origin OriginScope) bool { return o.origin != origin }

func (o recordOwner) String() string {
	if !o.isForwarding() {
		return o.origin.String()
	}
	return o.forwardedFor + recordOriginMarker + o.origin.String()
}

func (o recordOwner) comment() string {
	if !o.isForwarding() {
		return recordCommentHead + recordOriginMarker + o.origin.String()
	}
	return recordCommentHead + recordOwnerMarker + o.String()
}

func (o recordOwner) owns(rec dns.RecordResponse) bool { return rec.Comment == o.comment() }

func parseRecordOwner(comment string) (recordOwner, bool) {
	if written, isWriter := strings.CutPrefix(comment, recordCommentHead+recordOriginMarker); isWriter {
		origin, readable := parseOriginScope(written)
		return recordOwner{origin: origin}, readable
	}
	owner, forwarded := strings.CutPrefix(comment, recordCommentHead+recordOwnerMarker)
	if !forwarded {
		return recordOwner{}, false
	}
	forwardedFor, written, named := strings.Cut(owner, recordOriginMarker)
	if !named || forwardedFor == "" {
		return recordOwner{}, false
	}
	origin, readable := parseOriginScope(written)
	return recordOwner{origin: origin, forwardedFor: forwardedFor}, readable
}
