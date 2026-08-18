package dns

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	r53types "github.com/aws/aws-sdk-go-v2/service/route53/types"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const recordTTL = 300

type Route53API interface {
	ListHostedZones(ctx context.Context, in *route53.ListHostedZonesInput, optFns ...func(*route53.Options)) (*route53.ListHostedZonesOutput, error)
	ListResourceRecordSets(ctx context.Context, in *route53.ListResourceRecordSetsInput, optFns ...func(*route53.Options)) (*route53.ListResourceRecordSetsOutput, error)
	ChangeResourceRecordSets(ctx context.Context, in *route53.ChangeResourceRecordSetsInput, optFns ...func(*route53.Options)) (*route53.ChangeResourceRecordSetsOutput, error)
}

type route53Writer struct {
	api   Route53API
	named string

	mu   sync.Mutex
	seen []edge.Zone
}

var _ edge.DNSWriter = (*route53Writer)(nil)

func NewRoute53(api Route53API, zone string) edge.DNSWriter {
	return &route53Writer{api: api, named: zone}
}

func (w *route53Writer) EnsureRecords(ctx context.Context, records []edge.Record, _ func(string)) ([]edge.Record, error) {
	written := make([]edge.Record, 0, len(records))
	for _, want := range records {
		if want.Proxied {
			return written, fmt.Errorf("a proxied record only exists inside Cloudflare, so Route 53 cannot serve %s — front this deployment with an edge Route 53 can point at", want)
		}
		zone, err := w.zoneFor(ctx, want.Name)
		if err != nil {
			return written, err
		}
		if _, err := w.api.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
			HostedZoneId: aws.String(zone.ID),
			ChangeBatch: &r53types.ChangeBatch{Changes: []r53types.Change{{
				Action:            r53types.ChangeActionUpsert,
				ResourceRecordSet: recordSet(want),
			}}},
		}); err != nil {
			return written, fmt.Errorf("write DNS record %s in zone %s: %w", want, zone.Name, err)
		}
		written = append(written, want)
	}
	return written, nil
}

func (w *route53Writer) DeleteRecords(ctx context.Context, records []edge.Record) error {
	for _, written := range records {
		zone, err := w.zoneFor(ctx, written.Name)
		if err != nil {
			return err
		}
		live, err := w.liveSet(ctx, zone.ID, written)
		if err != nil {
			return err
		}
		if live == nil || len(live.ResourceRecords) != 1 || dnsName(aws.ToString(live.ResourceRecords[0].Value)) != written.Value {
			continue
		}
		if _, err := w.api.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
			HostedZoneId: aws.String(zone.ID),
			ChangeBatch: &r53types.ChangeBatch{Changes: []r53types.Change{{
				Action:            r53types.ChangeActionDelete,
				ResourceRecordSet: live,
			}}},
		}); err != nil {
			return fmt.Errorf("delete DNS record %s: %w", written, err)
		}
	}
	return nil
}

func (w *route53Writer) liveSet(ctx context.Context, zoneID string, want edge.Record) (*r53types.ResourceRecordSet, error) {
	out, err := w.api.ListResourceRecordSets(ctx, &route53.ListResourceRecordSetsInput{
		HostedZoneId:    aws.String(zoneID),
		StartRecordName: aws.String(want.Name),
		StartRecordType: r53types.RRType(want.Type),
	})
	if err != nil {
		return nil, fmt.Errorf("read DNS record %s: %w", want, err)
	}
	for _, set := range out.ResourceRecordSets {
		if !strings.EqualFold(dnsName(aws.ToString(set.Name)), want.Name) || string(set.Type) != string(want.Type) {
			continue
		}
		return &set, nil
	}
	return nil, nil
}

func recordSet(want edge.Record) *r53types.ResourceRecordSet {
	return &r53types.ResourceRecordSet{
		Name:            aws.String(want.Name),
		Type:            r53types.RRType(want.Type),
		TTL:             aws.Int64(recordTTL),
		ResourceRecords: []r53types.ResourceRecord{{Value: aws.String(want.Value)}},
	}
}

func (w *route53Writer) zoneFor(ctx context.Context, hostname string) (edge.Zone, error) {
	owned, err := w.hostedZones(ctx)
	if err != nil {
		return edge.Zone{}, err
	}
	return edge.SelectZone(owned, strings.TrimPrefix(hostname, "*."), w.named)
}

func (w *route53Writer) hostedZones(ctx context.Context) ([]edge.Zone, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.seen != nil {
		return w.seen, nil
	}
	owned := []edge.Zone{}
	var marker *string
	for {
		out, err := w.api.ListHostedZones(ctx, &route53.ListHostedZonesInput{Marker: marker})
		if err != nil {
			return nil, fmt.Errorf("list Route 53 hosted zones: %w", err)
		}
		for _, zone := range out.HostedZones {
			owned = append(owned, edge.Zone{
				ID:   strings.TrimPrefix(aws.ToString(zone.Id), "/hostedzone/"),
				Name: dnsName(aws.ToString(zone.Name)),
			})
		}
		if !out.IsTruncated || aws.ToString(out.NextMarker) == "" {
			w.seen = owned
			return owned, nil
		}
		marker = out.NextMarker
	}
}

func dnsName(name string) string {
	return unescapeOctal(strings.TrimSuffix(name, "."))
}

func unescapeOctal(name string) string {
	if !strings.Contains(name, `\`) {
		return name
	}
	var out strings.Builder
	out.Grow(len(name))
	for i := 0; i < len(name); i++ {
		if name[i] == '\\' && i+3 < len(name) && isOctal(name[i+1]) && isOctal(name[i+2]) && isOctal(name[i+3]) {
			out.WriteByte((name[i+1]-'0')<<6 | (name[i+2]-'0')<<3 | (name[i+3] - '0'))
			i += 3
			continue
		}
		out.WriteByte(name[i])
	}
	return out.String()
}

func isOctal(c byte) bool { return c >= '0' && c <= '7' }
