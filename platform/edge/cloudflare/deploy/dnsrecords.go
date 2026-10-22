package cloudflare

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/dns"
	"github.com/cloudflare/cloudflare-go/v4/option"
	"github.com/cloudflare/cloudflare-go/v4/packages/pagination"
	"github.com/cloudflare/cloudflare-go/v4/zones"

	"github.com/ocelhq/ocel/pkg/edge"
)

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

type dnsRecords struct {
	records recordAPI
	zones   zoneAPI

	accountID string
	named     string
	origin    func(context.Context) (OriginScope, error)

	originMu       sync.Mutex
	resolvedOrigin *OriginScope

	mu   sync.Mutex
	seen []edge.Zone
}

func NewDNS(zone string, origin func(context.Context) (OriginScope, error)) edge.DNSRecords {
	client := newAPIClient()
	return &dnsRecords{
		records:   client.DNS.Records,
		zones:     client.Zones,
		accountID: readAccountID(),
		named:     zone,
		origin:    origin,
	}
}

func (w *dnsRecords) originScope(ctx context.Context) (OriginScope, error) {
	w.originMu.Lock()
	defer w.originMu.Unlock()
	if w.resolvedOrigin != nil {
		return *w.resolvedOrigin, nil
	}
	scope, err := resolveOrigin(ctx, w.origin)
	if err != nil {
		return OriginScope{}, err
	}
	w.resolvedOrigin = &scope
	return scope, nil
}

func (w *dnsRecords) TTL() time.Duration {
	return automaticTTL
}

func (w *dnsRecords) VerifyCredentials(ctx context.Context) error {
	if err := refuseUnsetCredentials(w.accountID); err != nil {
		return err
	}
	params := zones.ZoneListParams{
		Account: cf.F(zones.ZoneListParamsAccount{ID: cf.F(w.accountID)}),
		PerPage: cf.F(float64(5)),
	}
	if w.named != "" {
		params.Name = cf.F(strings.ToLower(w.named))
	}
	res, err := w.zones.List(ctx, params)
	if err != nil {
		return refuseUnverifiedToken(ctx, w.accountID, err)
	}
	if w.named != "" && !slices.ContainsFunc(res.Result, func(z zones.Zone) bool { return strings.EqualFold(z.Name, w.named) }) {
		return fmt.Errorf("no zone named %q is reachable with %s in account %s", w.named, envAPIToken, w.accountID)
	}
	return nil
}

func (w *dnsRecords) Ensure(ctx context.Context, records []edge.Record, say func(string)) ([]edge.Record, error) {
	if len(records) == 0 {
		return []edge.Record{}, nil
	}
	origin, err := w.originScope(ctx)
	if err != nil {
		return nil, err
	}
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
		ours, err := w.ensure(ctx, zone, origin, want, live, say)
		if err != nil {
			return written, err
		}
		if ours {
			written = append(written, want)
		}
	}
	return written, nil
}

func (w *dnsRecords) ensure(ctx context.Context, zone edge.Zone, origin OriginScope, want edge.Record, live []dns.RecordResponse, say func(string)) (bool, error) {
	comment := formatWriterComment(origin)
	var mine, foreign *dns.RecordResponse
	for i := range live {
		if !isAddressRecord(live[i].Type) {
			continue
		}
		if live[i].Comment == comment {
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
		if err := w.replace(ctx, zone.ID, mine.ID, want, comment); err != nil {
			return false, err
		}
		return true, nil
	case foreign != nil:
		if !serves(*foreign, want) && say != nil {
			say(foreignRecordWarning(want, *foreign, origin))
		}
		return false, nil
	}
	body, err := recordBody(want, comment)
	if err != nil {
		return false, err
	}
	if _, err := w.records.New(ctx, dns.RecordNewParams{
		ZoneID: cf.F(zone.ID),
		Body:   body,
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

func foreignRecordWarning(want edge.Record, rec dns.RecordResponse, origin OriginScope) string {
	if held, written := parseRecordComment(rec.Comment); written && held.origin != origin {
		return fmt.Sprintf("%s already has a %s record that origin %s wrote, pointing at %s, and one hostname belongs to one origin, so this deployment will not serve it — release %s on %s first", want.Name, rec.Type, held.origin, rec.Content, want.Name, held.origin)
	}
	if want.Proxied {
		return fmt.Sprintf("%s already has a DNS record ocel did not write and it is not proxied through Cloudflare, so the worker route under it will never fire — set that record to proxied (orange cloud), or delete it and deploy again", want.Name)
	}
	return fmt.Sprintf("%s already has a %s record ocel did not write pointing at %s, so this deployment will not serve it — repoint it at %s, or delete it and deploy again", want.Name, rec.Type, rec.Content, want.Value)
}

func (w *dnsRecords) replace(ctx context.Context, zoneID, recordID string, want edge.Record, comment string) error {
	body, err := recordBody(want, comment)
	if err != nil {
		return err
	}
	if _, err := w.records.Update(ctx, recordID, dns.RecordUpdateParams{
		ZoneID: cf.F(zoneID),
		Body:   body,
	}); err != nil {
		return fmt.Errorf("repoint DNS record %s: %w", want, err)
	}
	return nil
}

type recordParam interface {
	dns.RecordNewParamsBodyUnion
	dns.RecordUpdateParamsBodyUnion
}

func recordBody(want edge.Record, comment string) (recordParam, error) {
	switch want.Type {
	case edge.RecordTypeCNAME:
		return dns.CNAMERecordParam{
			Name:    cf.F(want.Name),
			Type:    cf.F(dns.CNAMERecordTypeCNAME),
			Content: cf.F(want.Value),
			Proxied: cf.F(want.Proxied),
			TTL:     cf.F(dns.TTL(1)),
			Comment: cf.F(comment),
		}, nil
	case edge.RecordTypeA:
		return dns.ARecordParam{
			Name:    cf.F(want.Name),
			Type:    cf.F(dns.ARecordTypeA),
			Content: cf.F(want.Value),
			Proxied: cf.F(want.Proxied),
			TTL:     cf.F(dns.TTL(1)),
			Comment: cf.F(comment),
		}, nil
	case edge.RecordTypeAAAA:
		return dns.AAAARecordParam{
			Name:    cf.F(want.Name),
			Type:    cf.F(dns.AAAARecordTypeAAAA),
			Content: cf.F(want.Value),
			Proxied: cf.F(want.Proxied),
			TTL:     cf.F(dns.TTL(1)),
			Comment: cf.F(comment),
		}, nil
	}
	return nil, fmt.Errorf("write DNS record %s: %q is no record type this writer knows", want, want.Type)
}

func (w *dnsRecords) Delete(ctx context.Context, records []edge.Record) error {
	if len(records) == 0 {
		return nil
	}
	origin, err := w.originScope(ctx)
	if err != nil {
		return err
	}
	comment := formatWriterComment(origin)
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
			if rec.Comment != comment || rec.Content != written.Value || string(rec.Type) != string(written.Type) {
				continue
			}
			if _, err := w.records.Delete(ctx, rec.ID, dns.RecordDeleteParams{ZoneID: cf.F(zone.ID)}); err != nil {
				return fmt.Errorf("delete DNS record %s: %w", written, err)
			}
		}
	}
	return nil
}

func (w *dnsRecords) recordsAt(ctx context.Context, zoneID, hostname string) ([]dns.RecordResponse, error) {
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

func (w *dnsRecords) zoneFor(ctx context.Context, hostname string) (edge.Zone, error) {
	owned, err := w.ownedZones(ctx)
	if err != nil {
		return edge.Zone{}, err
	}
	return edge.SelectZone(owned, routeBaseDomain(hostname), w.named)
}

func (w *dnsRecords) ownedZones(ctx context.Context) ([]edge.Zone, error) {
	if w.accountID == "" {
		return nil, fmt.Errorf("%s is not set; it is required to write DNS records in Cloudflare", envAccountID)
	}
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
