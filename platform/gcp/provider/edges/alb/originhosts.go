package alb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

var originLimitAdvice = limitAdvice{
	remove: "remove promotions this tier no longer needs with `ocel promotions prune`",
	again:  "deploy again",
}

type OriginHost struct {
	Tier     environment.Tier
	Slug     string
	Hostname string
	Service  string
	Tag      string
	Revision string
}

type originHostRecord struct {
	Slug     string `json:"slug"`
	Service  string `json:"service"`
	Tag      string `json:"tag"`
	Revision string `json:"revision"`
	Backend  string `json:"backend"`
	Routed   bool   `json:"routed"`
}

func (e *Edge) originHostKey(tier environment.Tier, hostname string) keyvalue.Key {
	return stackrecords.EdgeStacksPartition(tier).Key(string(Kind), "origin-hosts", hostname)
}

type originRevisionRecord struct {
	Hostname string `json:"hostname"`
}

func (e *Edge) originRevisionPartition(tier environment.Tier) keyvalue.Partition {
	return stackrecords.EdgeStacksPartition(tier)
}

func (e *Edge) originRevisionKey(tier environment.Tier, service, revision string) keyvalue.Key {
	return e.originRevisionPartition(tier).Key(string(Kind), "origin-revisions", service, revision)
}

func (e *Edge) RouteOriginHost(ctx context.Context, host OriginHost) error {
	if !e.deps.Shielded {
		return refusal.Refuse(refusal.CodeInvalid,
			"only the shielded %s load balancer routes an origin hostname: it is the one that checks the worker's client certificate", Kind)
	}
	if host.Tier == "" || host.Slug == "" || host.Hostname == "" || host.Service == "" || host.Tag == "" || host.Revision == "" {
		return refusal.Refuse(refusal.CodeInvalid,
			"the %s load balancer routes an origin hostname to the revision tagged with its release, and this one names tier %q, project %q, hostname %q, service %q, tag %q and revision %q: "+
				"a host with no tag would reach the service's default traffic, not its deployment, and one with no revision could not be found again to unroute",
			Kind, host.Tier, host.Slug, host.Hostname, host.Service, host.Tag, host.Revision)
	}
	balancer, err := e.readProvisionedLoadBalancer(ctx, host.Tier)
	if err != nil {
		return err
	}
	record := originHostRecord{Slug: host.Slug, Service: host.Service, Tag: host.Tag, Revision: host.Revision, Backend: backendName(host.Slug, host.Tier, host.Hostname)}
	entry, err := keyvalue.ReadOrEmpty(ctx, e.deps.KeyValues, e.originHostKey(host.Tier, host.Hostname))
	if err != nil {
		return fmt.Errorf("read whether %s is routed on the %s edge: %w", host.Hostname, Kind, err)
	}
	if len(entry.Value) > 0 {
		var held originHostRecord
		if err := json.Unmarshal(entry.Value, &held); err != nil {
			return fmt.Errorf("decode what %s is routed to on the %s edge: %w", host.Hostname, Kind, err)
		}
		if routed := held; routed.Routed {
			routed.Routed = false
			if routed == record {
				return e.deps.Routes.Route(ctx, balancer.URLMap, host.Hostname, record.Backend)
			}
		}
	} else if err := e.refuseFullURLMap(ctx, balancer.URLMap, []string{host.Hostname}, 0, originLimitAdvice); err != nil {
		return err
	} else if err := e.refuseExhaustedBackendServiceQuota(ctx, []string{host.Hostname}, originLimitAdvice); err != nil {
		return err
	}
	if err := e.writeOriginRevisionRecord(ctx, host); err != nil {
		return err
	}
	if entry, err = e.writeOriginHostRecord(ctx, entry, record); err != nil {
		return err
	}
	target := Target{Tier: host.Tier, Slug: host.Slug, Origin: host.Hostname}
	if _, err := e.deps.Stacks.Up(ctx, target, originHostProgram(e.deps.Region, host, record), progress.Discard()); err != nil {
		return err
	}
	if err := e.deps.Routes.Route(ctx, balancer.URLMap, host.Hostname, record.Backend); err != nil {
		return err
	}
	record.Routed = true
	_, err = e.writeOriginHostRecord(ctx, entry, record)
	return err
}

func (e *Edge) writeOriginRevisionRecord(ctx context.Context, host OriginHost) error {
	entry, err := keyvalue.ReadOrEmpty(ctx, e.deps.KeyValues, e.originRevisionKey(host.Tier, host.Service, host.Revision))
	if err != nil {
		return fmt.Errorf("read which hostname revision %s answers on in the %s edge: %w", host.Revision, Kind, err)
	}
	if entry.Value, err = json.Marshal(originRevisionRecord{Hostname: host.Hostname}); err != nil {
		return err
	}
	if _, err := e.deps.KeyValues.Write(ctx, entry); err != nil {
		return fmt.Errorf("record which hostname revision %s answers on in the %s edge: %w", host.Revision, Kind, err)
	}
	return nil
}

func (e *Edge) writeOriginHostRecord(ctx context.Context, entry keyvalue.Entry, record originHostRecord) (keyvalue.Entry, error) {
	var err error
	if entry.Value, err = json.Marshal(record); err != nil {
		return entry, err
	}
	revision, err := e.deps.KeyValues.Write(ctx, entry)
	if err != nil {
		return entry, fmt.Errorf("record %s as routed on the %s edge: %w", record.Backend, Kind, err)
	}
	entry.Revision = revision
	return entry, nil
}

func (e *Edge) UnrouteOriginHosts(ctx context.Context, tier environment.Tier, service, revision string) error {
	under := []string{string(Kind), "origin-revisions", service}
	if revision != "" {
		under = append(under, revision)
	}
	entries, err := e.deps.KeyValues.List(ctx, e.originRevisionPartition(tier), under...)
	if err != nil {
		return fmt.Errorf("list the origin hostnames of %s on the %s edge: %w", service, Kind, err)
	}
	var errs []error
	for _, entry := range entries {
		var record originRevisionRecord
		if err := json.Unmarshal(entry.Value, &record); err != nil {
			errs = append(errs, fmt.Errorf("decode which hostname %s answers on in the %s edge: %w", entry.Key, Kind, err))
			continue
		}
		if superseded, err := e.isOriginHostServedByAnotherRevision(ctx, tier, record.Hostname, entry.Key.Path[len(entry.Key.Path)-1], revision != ""); err != nil {
			errs = append(errs, err)
			continue
		} else if superseded {
			errs = append(errs, keyvalue.Forget(ctx, e.deps.KeyValues, entry.Key))
			continue
		}
		if err := e.UnrouteOriginHost(ctx, tier, record.Hostname); err != nil {
			errs = append(errs, err)
			continue
		}
		errs = append(errs, keyvalue.Forget(ctx, e.deps.KeyValues, entry.Key))
	}
	return errors.Join(errs...)
}

func (e *Edge) isOriginHostServedByAnotherRevision(ctx context.Context, tier environment.Tier, hostname, revision string, single bool) (bool, error) {
	if !single {
		return false, nil
	}
	entry, err := e.deps.KeyValues.Read(ctx, e.originHostKey(tier, hostname))
	if errors.Is(err, keyvalue.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read whether %s is routed on the %s edge: %w", hostname, Kind, err)
	}
	var record originHostRecord
	if err := json.Unmarshal(entry.Value, &record); err != nil {
		return false, fmt.Errorf("decode what %s is routed to on the %s edge: %w", hostname, Kind, err)
	}
	return record.Revision != revision, nil
}

func (e *Edge) UnrouteOriginHost(ctx context.Context, tier environment.Tier, hostname string) error {
	entry, err := e.deps.KeyValues.Read(ctx, e.originHostKey(tier, hostname))
	if err != nil {
		if errors.Is(err, keyvalue.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("read whether %s is routed on the %s edge: %w", hostname, Kind, err)
	}
	var record originHostRecord
	if err := json.Unmarshal(entry.Value, &record); err != nil {
		return fmt.Errorf("decode what %s is routed to on the %s edge: %w", hostname, Kind, err)
	}
	outputs, err := e.deps.Stacks.Outputs(ctx, e.loadBalancerTarget(tier))
	if err != nil {
		return err
	}
	if balancer := loadBalancerOf(outputs); balancer.provisioned() {
		if err := e.deps.Routes.Unroute(ctx, balancer.URLMap, hostname); err != nil {
			return err
		}
	}
	if err := e.deps.Stacks.Destroy(ctx, Target{Tier: tier, Slug: record.Slug, Origin: hostname}, progress.Discard()); err != nil {
		return err
	}
	return keyvalue.Forget(ctx, e.deps.KeyValues, e.originHostKey(tier, hostname))
}

func originHostProgram(region string, host OriginHost, record originHostRecord) Program {
	return func(ctx *pulumi.Context, project string) error {
		return servingBackend(ctx, pulumi.String(project), region,
			negName(host.Slug, host.Tier, host.Hostname),
			Host{Service: host.Service, Tag: host.Tag, Backend: record.Backend}, nil)
	}
}
