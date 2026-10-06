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
	remove: "remove deployments this tier no longer needs with `ocel deployments prune`",
	again:  "deploy again",
}

type OriginHost struct {
	Tier     environment.Tier
	Slug     string
	Hostname string
	Service  string
	Tag      string
}

type originHostRecord struct {
	Slug    string `json:"slug"`
	Service string `json:"service"`
	Tag     string `json:"tag"`
	Backend string `json:"backend"`
}

func (e *Edge) originHostKey(tier environment.Tier, hostname string) keyvalue.Key {
	return stackrecords.EdgeStacksPartition(tier).Key(string(Kind), "origin-hosts", hostname)
}

func (e *Edge) RouteOriginHost(ctx context.Context, host OriginHost) error {
	if !e.deps.Shielded {
		return refusal.Refuse(refusal.CodeInvalid,
			"only the shielded %s load balancer routes an origin hostname: it is the one that checks the worker's client certificate", Kind)
	}
	if host.Tier == "" || host.Slug == "" || host.Hostname == "" || host.Service == "" || host.Tag == "" {
		return refusal.Refuse(refusal.CodeInvalid,
			"the %s load balancer routes an origin hostname to the revision tagged with its release, and this one names tier %q, project %q, hostname %q, service %q and tag %q: "+
				"a host with no tag would reach the service's default traffic, not its deployment",
			Kind, host.Tier, host.Slug, host.Hostname, host.Service, host.Tag)
	}
	balancer, err := e.readProvisionedLoadBalancer(ctx, host.Tier)
	if err != nil {
		return err
	}
	record := originHostRecord{Slug: host.Slug, Service: host.Service, Tag: host.Tag, Backend: backendName(host.Slug, host.Tier, host.Hostname)}
	entry, err := keyvalue.ReadOrEmpty(ctx, e.deps.KeyValues, e.originHostKey(host.Tier, host.Hostname))
	if err != nil {
		return fmt.Errorf("read whether %s is routed on the %s edge: %w", host.Hostname, Kind, err)
	}
	if len(entry.Value) > 0 {
		var held originHostRecord
		if err := json.Unmarshal(entry.Value, &held); err != nil {
			return fmt.Errorf("decode what %s is routed to on the %s edge: %w", host.Hostname, Kind, err)
		}
		if held == record {
			return e.deps.Routes.Route(ctx, balancer.URLMap, host.Hostname, record.Backend)
		}
	} else if err := e.refuseFullURLMap(ctx, balancer.URLMap, []string{host.Hostname}, 0, originLimitAdvice); err != nil {
		return err
	} else if err := e.refuseExhaustedBackendServiceQuota(ctx, []string{host.Hostname}, originLimitAdvice); err != nil {
		return err
	}
	target := Target{Tier: host.Tier, Slug: host.Slug, Origin: host.Hostname}
	if _, err := e.deps.Stacks.Up(ctx, target, originHostProgram(e.deps.Region, host, record), progress.Discard()); err != nil {
		return err
	}
	if err := e.deps.Routes.Route(ctx, balancer.URLMap, host.Hostname, record.Backend); err != nil {
		return err
	}
	if entry.Value, err = json.Marshal(record); err != nil {
		return err
	}
	if _, err := e.deps.KeyValues.Write(ctx, entry); err != nil {
		return fmt.Errorf("record that %s is routed on the %s edge: %w", host.Hostname, Kind, err)
	}
	return nil
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
