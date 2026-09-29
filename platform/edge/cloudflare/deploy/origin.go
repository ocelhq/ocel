package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/dns"
	"github.com/cloudflare/cloudflare-go/v4/zones"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

const ownerSeparator = " for "

func forwardingOwner(namespace, slug string, tier environment.Tier) string {
	return naming.NamespaceField(namespace) + fieldSeparator + slug + fieldSeparator + string(tier)
}

func ownerComment(owner string) string { return recordComment + ownerSeparator + owner }

func parseCommentOwner(comment string) (owner string, ocels bool) {
	if comment == recordComment {
		return "", true
	}
	owner, ocels = strings.CutPrefix(comment, recordComment+ownerSeparator)
	return owner, ocels && owner != ""
}

func proxiedRecord(hostname string, origin edge.Origin) (edge.Record, error) {
	records, err := edge.RecordsFor(edge.DNSTarget{
		Kind:           Kind,
		ProxiesRecords: true,
		FrontByHost:    map[string]string{hostname: origin.Address},
	}, []string{hostname})
	if err != nil {
		return edge.Record{}, err
	}
	if len(records) != 1 {
		return edge.Record{}, fmt.Errorf("%s names no record to forward to %s", hostname, origin.Address)
	}
	return records[0], nil
}

func (p *cloudflare) bindOrigin(ctx context.Context, state *edge.StackState, owner string, binding edge.DomainBinding) error {
	accountID := p.accountID()
	if accountID == "" {
		return fmt.Errorf("%s is not set; it is required to forward %s through Cloudflare", envAccountID, binding.Hostname)
	}
	if binding.Origin.Address == "" {
		return fmt.Errorf("the origin %s is forwarded to names no address", binding.Hostname)
	}
	want, err := p.forwardOrigin(ctx, accountID, owner, binding.Hostname, *binding.Origin)
	if err != nil {
		return err
	}
	state.Bind(binding.Hostname)
	state.PublishFront(binding.Hostname, binding.Origin.Address)
	state.RecordWrites(append(recordsBesides(state.Records, binding.Hostname), want))
	return nil
}

func (p *cloudflare) forwardOrigin(ctx context.Context, accountID, owner, hostname string, origin edge.Origin) (edge.Record, error) {
	if origin.Address == "" {
		return edge.Record{}, fmt.Errorf("the origin %s is forwarded to names no address", hostname)
	}
	zoneID, zoneName, err := p.resolveZone(ctx, accountID, routeBaseDomain(hostname))
	if err != nil {
		return edge.Record{}, err
	}
	if err := p.ensureTLSCover(ctx, zoneID, zoneName, hostname); err != nil {
		return edge.Record{}, err
	}
	if err := p.requireEncryptedOrigin(ctx, zoneID, zoneName); err != nil {
		return edge.Record{}, err
	}
	want, err := proxiedRecord(hostname, origin)
	if err != nil {
		return edge.Record{}, err
	}
	return want, p.forwardRecord(ctx, zoneID, owner, want)
}

func (p *cloudflare) unbindOrigin(ctx context.Context, state *edge.StackState, owner, hostname string) error {
	accountID := p.accountID()
	if accountID == "" {
		return fmt.Errorf("%s is not set; it is required to stop forwarding %s through Cloudflare", envAccountID, hostname)
	}
	zoneID, _, err := p.resolveZone(ctx, accountID, routeBaseDomain(hostname))
	if err != nil {
		return err
	}
	if err := p.dropForwardRecords(ctx, zoneID, owner, hostname); err != nil {
		return err
	}
	state.Release(hostname)
	state.PublishFront(hostname, "")
	state.RecordWrites(recordsBesides(state.Records, hostname))
	return nil
}

var plainOriginModes = []string{"off", "flexible"}

func (p *cloudflare) requireEncryptedOrigin(ctx context.Context, zoneID, zoneName string) error {
	setting, err := p.client.Zones.Settings.Get(ctx, "ssl", zones.SettingGetParams{ZoneID: cf.F(zoneID)})
	if err != nil {
		return fmt.Errorf("read the SSL mode of zone %s: %w", zoneName, err)
	}
	var read struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(setting.JSON.RawJSON()), &read); err != nil {
		return fmt.Errorf("read the SSL mode of zone %s: %w", zoneName, err)
	}
	if slices.Contains(plainOriginModes, read.Value) {
		return fmt.Errorf("zone %s reaches origins with SSL mode %q, which forwards over plain HTTP, and the origin answers only TLS carrying the client certificate the zone presents: set the zone's SSL mode to Full (strict) and bind it again", zoneName, read.Value)
	}
	return nil
}

func isForwarded(state edge.StackState, hostname string) bool {
	return slices.ContainsFunc(state.Records, func(rec edge.Record) bool { return rec.Name == hostname })
}

func recordsBesides(records []edge.Record, hostname string) []edge.Record {
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

func (p *cloudflare) forwardRecord(ctx context.Context, zoneID, owner string, want edge.Record) error {
	live, err := p.listAddressRecords(ctx, zoneID, want.Name)
	if err != nil {
		return err
	}
	var ours *dns.RecordResponse
	for i := range live {
		held, ocels := parseCommentOwner(live[i].Comment)
		switch {
		case !ocels:
			return fmt.Errorf("%s already has a %s record ocel did not write, pointing at %s — delete it, and ocel writes the proxied record that forwards %s through Cloudflare", want.Name, live[i].Type, live[i].Content, want.Name)
		case held != "" && held != owner:
			return fmt.Errorf("%s is forwarded through Cloudflare for %s, and one hostname is served by one project: release it there with `ocel domain remove` first", want.Name, held)
		}
		ours = &live[i]
	}
	body, err := recordBody(want)
	if err != nil {
		return err
	}
	body = withOwnerComment(body, owner)
	if ours == nil {
		if _, err := p.client.DNS.Records.New(ctx, dns.RecordNewParams{ZoneID: cf.F(zoneID), Body: body}); err != nil {
			return fmt.Errorf("write DNS record %s: %w", want, err)
		}
		return nil
	}
	if ours.Content == want.Value && string(ours.Type) == string(want.Type) && ours.Proxied && ours.Comment == ownerComment(owner) {
		return nil
	}
	if _, err := p.client.DNS.Records.Update(ctx, ours.ID, dns.RecordUpdateParams{ZoneID: cf.F(zoneID), Body: body}); err != nil {
		return fmt.Errorf("repoint DNS record %s: %w", want, err)
	}
	return nil
}

func withOwnerComment(body recordParam, owner string) recordParam {
	switch typed := body.(type) {
	case dns.ARecordParam:
		typed.Comment = cf.F(ownerComment(owner))
		return typed
	case dns.AAAARecordParam:
		typed.Comment = cf.F(ownerComment(owner))
		return typed
	case dns.CNAMERecordParam:
		typed.Comment = cf.F(ownerComment(owner))
		return typed
	}
	return body
}

func (p *cloudflare) dropForwardRecords(ctx context.Context, zoneID, owner, hostname string) error {
	live, err := p.listAddressRecords(ctx, zoneID, hostname)
	if err != nil {
		return err
	}
	var errs []error
	for _, rec := range live {
		if held, _ := parseCommentOwner(rec.Comment); held != owner {
			continue
		}
		if _, err := p.client.DNS.Records.Delete(ctx, rec.ID, dns.RecordDeleteParams{ZoneID: cf.F(zoneID)}); err != nil {
			errs = append(errs, fmt.Errorf("delete DNS record %s %s: %w", hostname, rec.Type, err))
		}
	}
	return errors.Join(errs...)
}

func (p *cloudflare) readForwardedOwner(ctx context.Context, zoneID, hostname string) (string, error) {
	live, err := p.listAddressRecords(ctx, zoneID, hostname)
	if err != nil {
		return "", err
	}
	for _, rec := range live {
		if held, _ := parseCommentOwner(rec.Comment); held != "" && rec.Proxied {
			return held, nil
		}
	}
	return "", nil
}
