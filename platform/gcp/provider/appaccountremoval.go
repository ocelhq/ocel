package gcp

import (
	"context"
	"fmt"
	"slices"

	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

func (p *Provider) forgetAppAccountIfUnused(ctx context.Context, ref provider.StackRef, progress progress.Log) error {
	c, err := p.openClients(ctx)
	if err != nil {
		return err
	}
	return forgetUnusedAppAccount(ctx, c, p.KeyValues(), ref, progress)
}

type revokedGrants struct {
	project []*cloudresourcemanager.Binding
	tasks   bool
	topics  []topics.Topology
}

func runsElsewhere(recorded []stackrecords.NamedStack, ref provider.StackRef) (naming.StackName, bool) {
	for _, stack := range recorded {
		if stack.Name != ref.Name && !stack.Name.IsInfra() && stack.Name.App == ref.Name.App {
			return stack.Name, true
		}
	}
	return naming.StackName{}, false
}

func forgetUnusedAppAccount(ctx context.Context, c *clients, records keyvalue.Store, ref provider.StackRef, progress progress.Log) error {
	if ref.Name.IsInfra() {
		return nil
	}
	recorded, err := stackrecords.List(ctx, records, ref.Tier, ref.Project)
	if err != nil {
		return err
	}
	if _, running := runsElsewhere(recorded, ref); running {
		return nil
	}
	member := "serviceAccount:" + c.AppAccountEmail(ref.Tier, ref.Project, ref.Name.App)
	var revoked revokedGrants
	if revoked.project, err = c.unbindProjectMember(ctx, member); err != nil {
		return err
	}
	taskCondition := taskDatabaseCondition(c, ref.Tier).Expression
	revoked.tasks = slices.ContainsFunc(revoked.project, func(b *cloudresourcemanager.Binding) bool {
		return b.Role == taskRecordsRole && b.Condition != nil && b.Condition.Expression == taskCondition
	})
	if err := absentIsDone(c.bindQueueRoles(ctx, ref.Tier, member, nil)); err != nil {
		return err
	}
	if err := absentIsDone(c.bindAccountRole(ctx, c.AppAccount(ref.Tier, ref.Project, ref.Name.App), runAsRole, member, false)); err != nil {
		return err
	}
	if revoked.topics, err = c.revokeTopicPublisher(ctx, recorded, ref, member); err != nil {
		return err
	}
	again, err := stackrecords.List(ctx, records, ref.Tier, ref.Project)
	if err != nil {
		return err
	}
	if other, running := runsElsewhere(again, ref); running {
		if err := c.restoreGrants(ctx, ref, member, revoked); err != nil {
			return err
		}
		if progress != nil {
			progress.Say(fmt.Sprintf("Kept what app %s of %s is granted in the %s tier: %s started running it while it was revoked", ref.Name.App, ref.Project, ref.Tier, other))
		}
		return nil
	}
	if progress != nil {
		progress.Say(fmt.Sprintf("Revoked what app %s of %s was granted in the %s tier: no environment runs it", ref.Name.App, ref.Project, ref.Tier))
	}
	return nil
}

func (c *clients) restoreGrants(ctx context.Context, ref provider.StackRef, member string, revoked revokedGrants) error {
	for _, binding := range revoked.project {
		if err := c.bindProjectRole(ctx, member, binding.Role, binding.Condition, true); err != nil {
			return err
		}
	}
	if revoked.tasks {
		if err := absentIsDone(c.bindQueueRoles(ctx, ref.Tier, member, queueRoles)); err != nil {
			return err
		}
		if err := absentIsDone(c.bindAccountRole(ctx, c.AppAccount(ref.Tier, ref.Project, ref.Name.App), runAsRole, member, true)); err != nil {
			return err
		}
	}
	for _, topology := range revoked.topics {
		if err := topology.GrantPublisher(ctx, c.Workload()); err != nil {
			return err
		}
	}
	return nil
}

func (c *clients) revokeTopicPublisher(ctx context.Context, recorded []stackrecords.NamedStack, ref provider.StackRef, member string) ([]topics.Topology, error) {
	var revoked []topics.Topology
	for _, stack := range recorded {
		if !stack.Name.IsInfra() {
			continue
		}
		declared := map[string]*provider.TopicSpec{}
		for _, binding := range stack.Bindings {
			topic, isTopic, err := readDeclaredTopic(binding)
			if err != nil {
				return nil, err
			}
			if isTopic {
				declared[topic.declared] = topic.spec
			}
		}
		if len(declared) == 0 {
			continue
		}
		topology := topics.Topology{
			Names:     taskNames(c.Names, provider.StackRef{Project: ref.Project, Tier: ref.Tier, Name: stack.Name}),
			Topics:    declared,
			Publisher: member,
		}
		taken, err := topology.RevokePublisher(ctx, c.Workload())
		if err != nil {
			return nil, err
		}
		if len(taken.Topics) > 0 {
			revoked = append(revoked, taken)
		}
	}
	return revoked, nil
}

func absentIsDone(err error) error {
	if err != nil && (absent(err) || status.Code(err) == codes.NotFound) {
		return nil
	}
	return err
}
