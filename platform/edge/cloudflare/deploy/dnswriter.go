package cloudflare

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/dns"
	"github.com/cloudflare/cloudflare-go/v4/option"
	"github.com/cloudflare/cloudflare-go/v4/packages/pagination"
	"github.com/cloudflare/cloudflare-go/v4/zones"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const recordComment = "managed by ocel"

const automaticTTL = 300 * time.Second

type recordAPI interface {
	List(ctx context.Context, params dns.RecordListParams, opts ...option.RequestOption) (*pagination.V4PagePaginationArray[dns.RecordResponse], error)
	New(ctx context.Context, params dns.RecordNewParams, opts ...option.RequestOption) (*dns.RecordResponse, error)
	Update(ctx context.Context, recordID string, params dns.RecordUpdateParams, opts ...option.RequestOption) (*dns.RecordResponse, error)
	Delete(ctx context.Context, recordID string, body dns.RecordDeleteParams, opts ...option.RequestOption) (*dns.RecordDeleteResponse, error)
}

type zoneAPI interface {
	List(ctx context.Context, params zones.ZoneListParams, opts ...option.RequestOption) (*pagination.V4PagePaginationArray[zones.Zone], error)
}

type dnsWriter struct {
	records recordAPI
	zones   zoneAPI

	accountID string
	named     string

	mu   sync.Mutex
	seen []edge.Zone
}

var _ edge.DNSWriter = (*dnsWriter)(nil)

func NewDNS(zone string) (edge.DNSWriter, error) {
	accountID := os.Getenv(envAccountID)
	if accountID == "" {
		return nil, fmt.Errorf("%s is not set; it is required to write DNS records in Cloudflare", envAccountID)
	}
	client := cf.NewClient(option.WithMaxRetries(clientMaxRetries))
	return &dnsWriter{
		records:   client.DNS.Records,
		zones:     client.Zones,
		accountID: accountID,
		named:     zone,
	}, nil
}

func (w *dnsWriter) RecordTTL() time.Duration {
	return automaticTTL
}

func (w *dnsWriter) ZoneOf(ctx context.Context, hostname string) (edge.Zone, error) {
	return w.zoneFor(ctx, hostname)
}

func (w *dnsWriter) EnsureRecords(ctx context.Context, records []edge.Record, say func(string)) ([]edge.Record, error) {
	written := make([]edge.Record, 0, len(records))
	for _, want := range records {
		zone, err := w.zoneFor(ctx, want.Name)
		if err != nil {
			return written, err
		}
		live, err := w.recordsAt(ctx, zone.ID, want.Name)
		if err != nil {
			return written, err
		}
		ours, err := w.ensure(ctx, zone, want, live, say)
		if err != nil {
			return written, err
		}
		if ours {
			written = append(written, want)
		}
	}
	return written, nil
}

func (w *dnsWriter) ensure(ctx context.Context, zone edge.Zone, want edge.Record, live []dns.RecordResponse, say func(string)) (bool, error) {
	var mine, foreign *dns.RecordResponse
	for i := range live {
		if !isAddressRecord(live[i].Type) {
			continue
		}
		if live[i].Comment == recordComment {
			mine = &live[i]
			break
		}
		if foreign == nil {
			foreign = &live[i]
		}
	}
	switch {
	case mine != nil:
		if mine.Content == want.Value && string(mine.Type) == string(want.Type) && mine.Proxied == want.Proxied {
			return true, nil
		}
		if err := w.replace(ctx, zone.ID, mine.ID, want); err != nil {
			return false, err
		}
		return true, nil
	case foreign != nil:
		if !serves(*foreign, want) && say != nil {
			say(foreignRecordWarning(want, *foreign))
		}
		return false, nil
	}
	if _, err := w.records.New(ctx, dns.RecordNewParams{
		ZoneID: cf.F(zone.ID),
		Body:   recordBody(want),
	}); err != nil {
		return false, fmt.Errorf("write DNS record %s in zone %s: %w", want, zone.Name, err)
	}
	return true, nil
}

func serves(rec dns.RecordResponse, want edge.Record) bool {
	if want.Proxied {
		return rec.Proxied
	}
	return string(rec.Type) == string(want.Type) && rec.Content == want.Value
}

func foreignRecordWarning(want edge.Record, rec dns.RecordResponse) string {
	if want.Proxied {
		return fmt.Sprintf("%s already has a DNS record ocel did not write and it is not proxied through Cloudflare, so the worker route under it will never fire — set that record to proxied (orange cloud), or delete it and deploy again", want.Name)
	}
	return fmt.Sprintf("%s already has a %s record ocel did not write pointing at %s, so this deployment will not serve it — repoint it at %s, or delete it and deploy again", want.Name, rec.Type, rec.Content, want.Value)
}

func (w *dnsWriter) replace(ctx context.Context, zoneID, recordID string, want edge.Record) error {
	if _, err := w.records.Update(ctx, recordID, dns.RecordUpdateParams{
		ZoneID: cf.F(zoneID),
		Body:   recordBody(want),
	}); err != nil {
		return fmt.Errorf("repoint DNS record %s: %w", want, err)
	}
	return nil
}

type recordParam interface {
	dns.RecordNewParamsBodyUnion
	dns.RecordUpdateParamsBodyUnion
}

func recordBody(want edge.Record) recordParam {
	switch want.Type {
	case edge.RecordTypeCNAME:
		return dns.CNAMERecordParam{
			Name:    cf.F(want.Name),
			Type:    cf.F(dns.CNAMERecordTypeCNAME),
			Content: cf.F(want.Value),
			Proxied: cf.F(want.Proxied),
			TTL:     cf.F(dns.TTL(1)),
			Comment: cf.F(recordComment),
		}
	case edge.RecordTypeA:
		return dns.ARecordParam{
			Name:    cf.F(want.Name),
			Type:    cf.F(dns.ARecordTypeA),
			Content: cf.F(want.Value),
			Proxied: cf.F(want.Proxied),
			TTL:     cf.F(dns.TTL(1)),
			Comment: cf.F(recordComment),
		}
	default:
		return dns.AAAARecordParam{
			Name:    cf.F(want.Name),
			Type:    cf.F(dns.AAAARecordTypeAAAA),
			Content: cf.F(want.Value),
			Proxied: cf.F(want.Proxied),
			TTL:     cf.F(dns.TTL(1)),
			Comment: cf.F(recordComment),
		}
	}
}

func (w *dnsWriter) DeleteRecords(ctx context.Context, records []edge.Record) error {
	for _, written := range records {
		zone, err := w.zoneFor(ctx, written.Name)
		if err != nil {
			return err
		}
		live, err := w.recordsAt(ctx, zone.ID, written.Name)
		if err != nil {
			return err
		}
		for _, rec := range live {
			if rec.Content != written.Value || string(rec.Type) != string(written.Type) {
				continue
			}
			if _, err := w.records.Delete(ctx, rec.ID, dns.RecordDeleteParams{ZoneID: cf.F(zone.ID)}); err != nil {
				return fmt.Errorf("delete DNS record %s: %w", written, err)
			}
		}
	}
	return nil
}

func (w *dnsWriter) recordsAt(ctx context.Context, zoneID, hostname string) ([]dns.RecordResponse, error) {
	var found []dns.RecordResponse
	for page := 1; ; page++ {
		res, err := w.records.List(ctx, dns.RecordListParams{
			ZoneID: cf.F(zoneID),
			Name:   cf.F(dns.RecordListParamsName{Exact: cf.F(hostname)}),
			Page:   cf.F(float64(page)),
		})
		if err != nil {
			return nil, fmt.Errorf("list DNS records for %q: %w", hostname, err)
		}
		if len(res.Result) == 0 {
			return found, nil
		}
		found = append(found, res.Result...)
	}
}

func (w *dnsWriter) zoneFor(ctx context.Context, hostname string) (edge.Zone, error) {
	owned, err := w.ownedZones(ctx)
	if err != nil {
		return edge.Zone{}, err
	}
	return edge.SelectZone(owned, routeBaseDomain(hostname), w.named)
}

func (w *dnsWriter) ownedZones(ctx context.Context) ([]edge.Zone, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.seen != nil {
		return w.seen, nil
	}
	owned := []edge.Zone{}
	for page := 1; ; page++ {
		res, err := w.zones.List(ctx, zones.ZoneListParams{
			Account: cf.F(zones.ZoneListParamsAccount{ID: cf.F(w.accountID)}),
			Page:    cf.F(float64(page)),
		})
		if err != nil {
			return nil, fmt.Errorf("list zones: %w", err)
		}
		if len(res.Result) == 0 {
			w.seen = owned
			return owned, nil
		}
		for _, z := range res.Result {
			owned = append(owned, edge.Zone{ID: z.ID, Name: z.Name})
		}
	}
}
