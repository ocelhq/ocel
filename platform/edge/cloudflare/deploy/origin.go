package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/dns"
	"github.com/cloudflare/cloudflare-go/v4/zones"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func newProxiedRecord(hostname string, origin edge.Origin) (edge.Record, error) {
	records, err := edge.RecordsFor(edge.DNSTarget{
		Kind:           Kind,
		ProxiesRecords: true,
		AddressByHost:  map[string]string{hostname: origin.Address},
	}, []string{hostname})
	if err != nil {
		return edge.Record{}, err
	}
	if len(records) != 1 {
		return edge.Record{}, fmt.Errorf("%s names no record to forward to %s", hostname, origin.Address)
	}
	return records[0], nil
}

func (p *cloudflare) bindOrigin(ctx context.Context, state *edge.StackState, zone zoneAccess, owner recordOwner, binding edge.DomainBinding) error {
	want, err := p.forwardOrigin(ctx, zone, owner, binding.Hostname, *binding.Origin)
	if err != nil {
		return err
	}
	state.Bind(binding.Hostname)
	state.PublishAddress(binding.Hostname, binding.Origin.Address)
	state.RecordWrites(append(removeRecordsNamed(state.Records, binding.Hostname), want))
	return nil
}

func (p *cloudflare) forwardOrigin(ctx context.Context, zone zoneAccess, owner recordOwner, hostname string, forwardedTo edge.Origin) (edge.Record, error) {
	if forwardedTo.Address == "" {
		return edge.Record{}, fmt.Errorf("the origin %s is forwarded to names no address", hostname)
	}
	if err := p.orderTLSCover(ctx, zone.zoneID, zone.zoneName, hostname); err != nil {
		return edge.Record{}, err
	}
	if !forwardedTo.Tunneled {
		if err := p.requireStrictOrigin(ctx, zone); err != nil {
			return edge.Record{}, err
		}
	}
	want, err := newProxiedRecord(hostname, forwardedTo)
	if err != nil {
		return edge.Record{}, err
	}
	return want, p.forwardRecord(ctx, zone.zoneID, owner, want)
}

func (p *cloudflare) unbindOrigin(ctx context.Context, state *edge.StackState, zone zoneAccess, owner recordOwner, hostname string) error {
	if err := p.dropForwardRecords(ctx, zone.zoneID, owner, hostname); err != nil {
		return err
	}
	if err := p.releaseClientCertificate(ctx, zone, owner.origin, hostname); err != nil {
		return err
	}
	state.Release(hostname)
	state.PublishAddress(hostname, "")
	state.RecordWrites(removeRecordsNamed(state.Records, hostname))
	return nil
}

var strictOriginModes = []string{string(zones.SSLValueStrict), string(zones.SSLValueOriginPull)}

func (p *cloudflare) requireStrictOrigin(ctx context.Context, zone zoneAccess) error {
	setting, err := p.client.Zones.Settings.Get(ctx, "ssl", zones.SettingGetParams{ZoneID: cf.F(zone.zoneID)})
	if err != nil {
		return fmt.Errorf("read the SSL mode of zone %s: %w", zone.zoneName, err)
	}
	var read struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(setting.JSON.RawJSON()), &read); err != nil {
		return fmt.Errorf("read the SSL mode of zone %s: %w", zone.zoneName, err)
	}
	if !slices.Contains(strictOriginModes, read.Value) {
		return refusal.Refuse(refusal.CodeInvalid,
			"zone %s reaches origins with SSL mode %q, which does not check the certificate the origin answers with, so Cloudflare would forward to anything answering its address: set the zone's SSL mode to Full (strict) and try again",
			zone.zoneName, read.Value)
	}
	return nil
}

func removeRecordsNamed(records []edge.Record, hostname string) []edge.Record {
	return slices.DeleteFunc(slices.Clone(records), func(rec edge.Record) bool { return rec.Name == hostname })
}

func (p *cloudflare) listAddressRecords(ctx context.Context, zoneID, hostname string) ([]dns.RecordResponse, error) {
	listed := p.client.DNS.Records.ListAutoPaging(ctx, dns.RecordListParams{
		ZoneID: cf.F(zoneID),
		Name:   cf.F(dns.RecordListParamsName{Exact: cf.F(hostname)}),
	})
	var found []dns.RecordResponse
	for listed.Next() {
		if rec := listed.Current(); isAddressRecord(rec.Type) {
			found = append(found, rec)
		}
	}
	if err := listed.Err(); err != nil {
		return nil, fmt.Errorf("list DNS records for %q: %w", hostname, err)
	}
	return found, nil
}

func (p *cloudflare) forwardRecord(ctx context.Context, zoneID string, owner recordOwner, want edge.Record) error {
	live, err := p.listAddressRecords(ctx, zoneID, want.Name)
	if err != nil {
		return err
	}
	for _, rec := range live {
		if !owner.owns(rec) && !dnsWriter(owner.origin).owns(rec) {
			return refuseHeldRecord(want.Name, rec, owner.origin)
		}
	}
	body, err := recordBody(want, owner.comment())
	if err != nil {
		return err
	}
	if len(live) == 0 {
		if _, err := p.client.DNS.Records.New(ctx, dns.RecordNewParams{ZoneID: cf.F(zoneID), Body: body}); err != nil {
			return fmt.Errorf("write DNS record %s: %w", want, err)
		}
		return nil
	}
	ours, extra := live[0], live[1:]
	if ours.Content != want.Value || string(ours.Type) != string(want.Type) || !ours.Proxied || !owner.owns(ours) {
		if _, err := p.client.DNS.Records.Update(ctx, ours.ID, dns.RecordUpdateParams{ZoneID: cf.F(zoneID), Body: body}); err != nil {
			return fmt.Errorf("repoint DNS record %s: %w", want, err)
		}
	}
	var errs []error
	for _, rec := range extra {
		if _, err := p.client.DNS.Records.Delete(ctx, rec.ID, dns.RecordDeleteParams{ZoneID: cf.F(zoneID)}); err != nil {
			errs = append(errs, fmt.Errorf("delete the second DNS record %s %s that forwards it: %w", want.Name, rec.Type, err))
		}
	}
	return errors.Join(errs...)
}

func refuseHeldRecord(hostname string, rec dns.RecordResponse, origin OriginScope) error {
	held, written := parseRecordOwner(rec.Comment)
	switch {
	case !written:
		return fmt.Errorf("%s already has a %s record ocel did not write, pointing at %s — delete it, and ocel writes the proxied record that forwards %s through Cloudflare", hostname, rec.Type, rec.Content, hostname)
	case held.isOtherOrigin(origin) && held.isForwarding():
		return refuseOtherOrigin(fmt.Sprintf("%s is forwarded through Cloudflare for %s", hostname, held), hostname, held.origin)
	case held.isOtherOrigin(origin):
		return refuseOtherOrigin(fmt.Sprintf("%s already has a %s record that origin %s wrote, pointing at %s", hostname, rec.Type, held.origin, rec.Content), hostname, held.origin)
	}
	return fmt.Errorf("%s is forwarded through Cloudflare for %s, and one hostname is served by one project: release it there with `ocel domain remove` first", hostname, held)
}

func (p *cloudflare) dropForwardRecords(ctx context.Context, zoneID string, owner recordOwner, hostname string) error {
	live, err := p.listAddressRecords(ctx, zoneID, hostname)
	if err != nil {
		return err
	}
	var errs []error
	for _, rec := range live {
		if !owner.owns(rec) {
			continue
		}
		if _, err := p.client.DNS.Records.Delete(ctx, rec.ID, dns.RecordDeleteParams{ZoneID: cf.F(zoneID)}); err != nil {
			errs = append(errs, fmt.Errorf("delete DNS record %s %s: %w", hostname, rec.Type, err))
		}
	}
	return errors.Join(errs...)
}

func (p *cloudflare) isForwardedBy(ctx context.Context, zoneID string, owner recordOwner, hostname string) (bool, error) {
	live, err := p.listAddressRecords(ctx, zoneID, hostname)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(live, owner.owns), nil
}

func (p *cloudflare) unforwardRecords(ctx context.Context, zoneID string, owner recordOwner, hostname string) error {
	live, err := p.listAddressRecords(ctx, zoneID, hostname)
	if err != nil {
		return err
	}
	body, err := recordBody(edge.Record{Name: hostname, Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}, dnsWriter(owner.origin).comment())
	if err != nil {
		return err
	}
	var errs []error
	rewritten := false
	for _, rec := range live {
		if !owner.owns(rec) {
			continue
		}
		if rewritten {
			if _, err := p.client.DNS.Records.Delete(ctx, rec.ID, dns.RecordDeleteParams{ZoneID: cf.F(zoneID)}); err != nil {
				errs = append(errs, fmt.Errorf("delete DNS record %s %s: %w", hostname, rec.Type, err))
			}
			continue
		}
		if _, err := p.client.DNS.Records.Update(ctx, rec.ID, dns.RecordUpdateParams{ZoneID: cf.F(zoneID), Body: body}); err != nil {
			return fmt.Errorf("repoint DNS record %s off its origin: %w", hostname, err)
		}
		rewritten = true
	}
	return errors.Join(errs...)
}

func (p *cloudflare) readForwardingOwner(ctx context.Context, zoneID, hostname string) (recordOwner, bool, error) {
	live, err := p.listAddressRecords(ctx, zoneID, hostname)
	if err != nil {
		return recordOwner{}, false, err
	}
	for _, rec := range live {
		if held, written := parseRecordOwner(rec.Comment); written && held.isForwarding() && rec.Proxied {
			return held, true, nil
		}
	}
	return recordOwner{}, false, nil
}

func domainOwner(forwarding recordOwner, origin OriginScope) string {
	if forwarding == previewForwardingOwner(origin) {
		return edge.PreviewEntryOwner
	}
	return forwarding.String()
}
