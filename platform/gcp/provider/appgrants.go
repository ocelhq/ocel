package gcp

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/platform/gcp/provider/iampolicy"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

type targetKind string

const (
	targetProject targetKind = "project"
	targetKey     targetKind = "key"
	targetAccount targetKind = "account"
	targetBucket  targetKind = "bucket"
	targetQueue   targetKind = "queue"
	targetTopic   targetKind = "topic"
	targetSecret  targetKind = "secret"
)

var targetOrder = []targetKind{targetProject, targetKey, targetAccount, targetQueue, targetTopic, targetBucket, targetSecret}

const appGrantsRecord = "appgrants"

type grantTarget struct {
	Kind targetKind `json:"kind"`
	Name string     `json:"name,omitempty"`
}

type appGrant struct {
	Target    grantTarget          `json:"target"`
	Role      string               `json:"role"`
	Member    string               `json:"member"`
	Condition *iampolicy.Condition `json:"condition,omitempty"`
}

func (g appGrant) same(other appGrant) bool {
	return g.Target == other.Target && g.Role == other.Role && g.Member == other.Member &&
		conditionExpression(g.Condition) == conditionExpression(other.Condition)
}

func conditionExpression(condition *iampolicy.Condition) string {
	if condition == nil {
		return ""
	}
	return condition.Expression
}

func onProject(role, member string, condition *iampolicy.Condition) appGrant {
	return appGrant{Target: grantTarget{Kind: targetProject}, Role: role, Member: member, Condition: condition}
}

func on(kind targetKind, name, role, member string) appGrant {
	return appGrant{Target: grantTarget{Kind: kind, Name: name}, Role: role, Member: member}
}

func withoutGrants(grants, taken []appGrant) []appGrant {
	return slices.DeleteFunc(slices.Clone(grants), func(grant appGrant) bool {
		return slices.ContainsFunc(taken, grant.same)
	})
}

func unionOf(grants ...[]appGrant) []appGrant {
	var union []appGrant
	for _, each := range grants {
		for _, grant := range each {
			if !slices.ContainsFunc(union, grant.same) {
				union = append(union, grant)
			}
		}
	}
	return union
}

func sameGrants(a, b []appGrant) bool {
	return len(withoutGrants(a, b)) == 0 && len(withoutGrants(b, a)) == 0
}

func targetsOf(grants []appGrant) []grantTarget {
	var targets []grantTarget
	for _, grant := range grants {
		if !slices.Contains(targets, grant.Target) {
			targets = append(targets, grant.Target)
		}
	}
	slices.SortStableFunc(targets, func(a, b grantTarget) int {
		if order := cmp.Compare(slices.Index(targetOrder, a.Kind), slices.Index(targetOrder, b.Kind)); order != 0 {
			return order
		}
		return cmp.Compare(a.Name, b.Name)
	})
	return targets
}

func appGrantsKey(tier environment.Tier, account string) keyvalue.Key {
	return keyvalue.Partition{Tier: tier, Root: keyvalue.RootBootstrap}.Key(appGrantsRecord, account)
}

func readAppGrants(ctx context.Context, records keyvalue.Store, tier environment.Tier, account string) ([]appGrant, error) {
	recorded, err := keyvalue.ReadOrEmpty(ctx, records, appGrantsKey(tier, account))
	if err != nil || len(recorded.Value) == 0 {
		return nil, err
	}
	var grants []appGrant
	if err := json.Unmarshal(recorded.Value, &grants); err != nil {
		return nil, fmt.Errorf("read what the %s service account was granted: %w", account, err)
	}
	return grants, nil
}

func recordAppGrants(ctx context.Context, records keyvalue.Store, tier environment.Tier, account string, grants []appGrant) error {
	encoded, err := json.Marshal(grants)
	if err != nil {
		return err
	}
	return keyvalue.Change(ctx, records, appGrantsKey(tier, account), func(recorded keyvalue.Entry) ([]byte, bool, error) {
		return encoded, string(recorded.Value) != string(encoded), nil
	})
}

func (c *clients) policyOf(target grantTarget) iampolicy.Resource {
	switch target.Kind {
	case targetProject:
		return c.projectPolicy()
	case targetKey:
		return c.keyPolicy(target.Name, target.Name)
	case targetAccount:
		return c.accountPolicy(target.Name)
	case targetBucket:
		return c.bucketPolicy(target.Name)
	case targetQueue:
		return c.queuePolicy(target.Name, "the delay queue "+target.Name)
	case targetSecret:
		return c.secretPolicy(target.Name)
	}
	return iampolicy.Resource{Name: "the Pub/Sub topic " + target.Name, Read: func(ctx context.Context) ([]iampolicy.Binding, iampolicy.Write, error) {
		service, err := c.Workload().PubSub()
		if err != nil {
			return nil, nil, err
		}
		return topics.TopicPolicy(service, target.Name).Read(ctx)
	}}
}

func (c *clients) explainRefusedGrant(tier environment.Tier, target grantTarget, err error) error {
	switch target.Kind {
	case targetProject:
		if isMissingRole(err, c.CDNPurgeRolePath()) {
			return refuseMissingCDNPurgeRole(c, tier)
		}
		return explainMissingGrant(err, appGrantsGrant(c.Names))
	case targetAccount:
		return wrapAccountGrantError(err, c.AppAccountsRolePath())
	case targetQueue:
		return explainMissingGrant(err, queueAdminGrant(target.Name))
	}
	return err
}

func changesOn(target grantTarget, granted, revoked []appGrant) iampolicy.Change {
	var changes []iampolicy.Change
	for _, grant := range granted {
		if grant.Target == target {
			changes = append(changes, iampolicy.Grant(grant.Role, grant.Member, grant.Condition))
		}
	}
	for _, grant := range revoked {
		if grant.Target == target {
			changes = append(changes, iampolicy.Revoke(grant.Role, grant.Member, grant.Condition))
		}
	}
	return iampolicy.Each(changes...)
}

func (c *clients) applyGrants(ctx context.Context, tier environment.Tier, granted, revoked []appGrant) error {
	for _, target := range targetsOf(slices.Concat(granted, revoked)) {
		granting := slices.ContainsFunc(granted, func(grant appGrant) bool { return grant.Target == target })
		err := untilVisible(ctx, func() error {
			_, err := iampolicy.Edit(ctx, c.policyOf(target), changesOn(target, granted, revoked))
			if err != nil && (granting || !iampolicy.IsAbsent(err)) {
				return c.explainRefusedGrant(tier, target, err)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (p *Provider) reconcileAppGrants(ctx context.Context, c *clients, tier environment.Tier, account string, want []appGrant) error {
	recorded, err := readAppGrants(ctx, p.KeyValues(), tier, account)
	if err != nil {
		return err
	}
	held := unionOf(recorded, want)
	if !sameGrants(held, recorded) {
		if err := recordAppGrants(ctx, p.KeyValues(), tier, account, held); err != nil {
			return err
		}
	}
	if err := c.applyGrants(ctx, tier, want, withoutGrants(recorded, want)); err != nil {
		return err
	}
	if sameGrants(held, want) {
		return nil
	}
	return recordAppGrants(ctx, p.KeyValues(), tier, account, want)
}

func revokeAppGrants(ctx context.Context, c *clients, records keyvalue.Store, tier environment.Tier, account string) (bool, error) {
	recorded, err := readAppGrants(ctx, records, tier, account)
	if err != nil {
		return false, err
	}
	if err := c.applyGrants(ctx, tier, nil, recorded); err != nil {
		return false, err
	}
	return len(recorded) > 0, keyvalue.Forget(ctx, records, appGrantsKey(tier, account))
}

func ignoreAbsentPolicy(err error) error {
	if iampolicy.IsAbsent(err) {
		return nil
	}
	return err
}
