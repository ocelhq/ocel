package grants

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/credential"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
	"google.golang.org/api/googleapi"
)

type targetKind string

const (
	Project targetKind = "project"
	Key     targetKind = "key"
	Account targetKind = "account"
	Bucket  targetKind = "bucket"
	Queue   targetKind = "queue"
	Topic   targetKind = "topic"
	Secret  targetKind = "secret"
)

var targetOrder = []targetKind{Project, Key, Account, Queue, Topic, Bucket, Secret}

const appGrantsRecord = "appgrants"

const (
	OpeningRole = "roles/cloudkms.cryptoKeyDecrypter"
	ObjectsRole = "roles/storage.objectUser"
	AssetsRole  = "roles/storage.objectViewer"
)

const TokenCreatorRole = "roles/iam.serviceAccountTokenCreator"

type grantTarget struct {
	Kind targetKind `json:"kind"`
	Name string     `json:"name,omitempty"`
}

type Grant struct {
	Target    grantTarget       `json:"target"`
	Role      string            `json:"role"`
	Member    string            `json:"member"`
	Condition *policy.Condition `json:"condition,omitempty"`
}

func (g Grant) same(other Grant) bool {
	return g.Target == other.Target && g.Role == other.Role && g.Member == other.Member &&
		conditionExpression(g.Condition) == conditionExpression(other.Condition)
}

func conditionExpression(condition *policy.Condition) string {
	if condition == nil {
		return ""
	}
	return condition.Expression
}

func OnProject(role, member string, condition *policy.Condition) Grant {
	return Grant{Target: grantTarget{Kind: Project}, Role: role, Member: member, Condition: condition}
}

func On(kind targetKind, name, role, member string) Grant {
	return Grant{Target: grantTarget{Kind: kind, Name: name}, Role: role, Member: member}
}

func OnConditioned(kind targetKind, name, role, member string, condition *policy.Condition) Grant {
	return Grant{Target: grantTarget{Kind: kind, Name: name}, Role: role, Member: member, Condition: condition}
}

func OnObjects(title string, buckets naming.Buckets, scope naming.StorageScope, role, member string, kinds ...naming.ObjectKind) ([]Grant, error) {
	var held []string
	byBucket := map[string][]naming.ObjectKind{}
	for _, kind := range kinds {
		bucket := buckets.Holding(kind)
		if _, seen := byBucket[bucket]; !seen {
			held = append(held, bucket)
		}
		byBucket[bucket] = append(byBucket[bucket], kind)
	}
	granted := make([]Grant, 0, len(held))
	for _, bucket := range held {
		condition, err := gcloud.ObjectKindsCondition(title, buckets, scope, byBucket[bucket]...)
		if err != nil {
			return nil, err
		}
		granted = append(granted, OnConditioned(Bucket, bucket, role, member, condition))
	}
	return granted, nil
}

func withoutGrants(grants, taken []Grant) []Grant {
	return slices.DeleteFunc(slices.Clone(grants), func(grant Grant) bool {
		return slices.ContainsFunc(taken, grant.same)
	})
}

func unionOf(grants ...[]Grant) []Grant {
	var union []Grant
	for _, each := range grants {
		for _, grant := range each {
			if !slices.ContainsFunc(union, grant.same) {
				union = append(union, grant)
			}
		}
	}
	return union
}

func sameGrants(a, b []Grant) bool {
	return len(withoutGrants(a, b)) == 0 && len(withoutGrants(b, a)) == 0
}

func targetsOf(grants []Grant) []grantTarget {
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

func appGrantsKey(tier environment.Tier, account string) records.Key {
	return records.Partition{Tier: tier, Root: stackrecords.RootBootstrap}.Key(appGrantsRecord, account)
}

func Read(ctx context.Context, store records.Store, tier environment.Tier, account string) ([]Grant, error) {
	recorded, err := records.ReadOrEmpty(ctx, store, appGrantsKey(tier, account))
	if err != nil || len(recorded.Value) == 0 {
		return nil, err
	}
	var grants []Grant
	if err := json.Unmarshal(recorded.Value, &grants); err != nil {
		return nil, fmt.Errorf("read what the %s service account was granted: %w", account, err)
	}
	return grants, nil
}

func recordAppGrants(ctx context.Context, store records.Store, tier environment.Tier, account string, grants []Grant) error {
	encoded, err := json.Marshal(grants)
	if err != nil {
		return err
	}
	return records.Change(ctx, store, appGrantsKey(tier, account), func(recorded records.Entry) ([]byte, bool, error) {
		return encoded, string(recorded.Value) != string(encoded), nil
	})
}

func policyOf(c *gcloud.Clients, target grantTarget) policy.Resource {
	switch target.Kind {
	case Project:
		return gcloud.ProjectPolicy(c)
	case Key:
		return gcloud.KeyPolicy(c, target.Name, target.Name)
	case Account:
		return gcloud.AccountPolicy(c, target.Name)
	case Bucket:
		return gcloud.BucketPolicy(c, target.Name)
	case Queue:
		return gcloud.QueuePolicy(c, target.Name, "the delay queue "+target.Name)
	case Secret:
		return gcloud.SecretPolicy(c, target.Name)
	}
	return policy.Resource{Name: "the Pub/Sub topic " + target.Name, Read: func(ctx context.Context) ([]policy.Binding, policy.Write, error) {
		service, err := c.Workload().PubSub()
		if err != nil {
			return nil, nil, err
		}
		return topics.TopicPolicy(service, target.Name).Read(ctx)
	}}
}

func explainRefusedGrant(c *gcloud.Clients, tier environment.Tier, target grantTarget, err error) error {
	deploy := c.DeployCredential(tier)
	switch target.Kind {
	case Project:
		if isMissingRole(err, c.CDNPurgeRolePath()) {
			return refuseMissingCDNPurgeRole(c, tier)
		}
		return gcloud.ExplainMissingGrant(err, deploy.AppGrants().String())
	case Account:
		return gcloud.WrapAccountGrantError(err, deploy.Tiered(credential.AccountsRole).String())
	case Queue:
		return gcloud.ExplainMissingGrant(err, deploy.Queue().String())
	}
	return err
}

func changesOn(target grantTarget, granted, revoked []Grant) policy.Change {
	var changes []policy.Change
	for _, grant := range granted {
		if grant.Target == target {
			changes = append(changes, policy.Grant(grant.Role, grant.Member, grant.Condition))
		}
	}
	for _, grant := range revoked {
		if grant.Target == target {
			changes = append(changes, policy.Revoke(grant.Role, grant.Member, grant.Condition))
		}
	}
	return policy.Each(changes...)
}

func Apply(ctx context.Context, c *gcloud.Clients, tier environment.Tier, granted, revoked []Grant) error {
	for _, target := range targetsOf(slices.Concat(granted, revoked)) {
		granting := slices.ContainsFunc(granted, func(grant Grant) bool { return grant.Target == target })
		err := gcloud.UntilVisible(ctx, func() error {
			_, err := policy.Edit(ctx, policyOf(c, target), changesOn(target, granted, revoked))
			if err != nil && (granting || !policy.IsAbsent(err)) {
				return explainRefusedGrant(c, tier, target, err)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func Reconcile(ctx context.Context, c *gcloud.Clients, store records.Store, tier environment.Tier, account string, want []Grant) error {
	recorded, err := Read(ctx, store, tier, account)
	if err != nil {
		return err
	}
	held := unionOf(recorded, want)
	if !sameGrants(held, recorded) {
		if err := recordAppGrants(ctx, store, tier, account, held); err != nil {
			return err
		}
	}
	if err := Apply(ctx, c, tier, want, withoutGrants(recorded, want)); err != nil {
		return err
	}
	if sameGrants(held, want) {
		return nil
	}
	return recordAppGrants(ctx, store, tier, account, want)
}

func RevokeAll(ctx context.Context, c *gcloud.Clients, store records.Store, tier environment.Tier, account string) (bool, error) {
	recorded, err := Read(ctx, store, tier, account)
	if err != nil {
		return false, err
	}
	if err := Apply(ctx, c, tier, nil, recorded); err != nil {
		return false, err
	}
	return len(recorded) > 0, records.Forget(ctx, store, appGrantsKey(tier, account))
}

func isMissingRole(err error, role string) bool {
	var answered *googleapi.Error
	return errors.As(err, &answered) && answered.Code == 400 && strings.Contains(answered.Message, role)
}

func refuseMissingCDNPurgeRole(c *gcloud.Clients, tier environment.Tier) error {
	return refusal.Refuse(refusal.CodeNotReady,
		"the custom role %s that lets an app that revalidates behind the alb clear Cloud CDN is not in project %s: the %s bootstrap predates it.\n"+
			"Run `%s`, then deploy again", c.CDNPurgeRolePath(), c.Project(), tier, provider.BootstrapCommand(tier))
}
