package gcp

import (
	"context"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/keyvalue"
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

func forgetUnusedAppAccount(ctx context.Context, c *clients, records keyvalue.Store, ref provider.StackRef, progress progress.Log) error {
	if ref.Name.IsInfra() {
		return nil
	}
	recorded, err := stackrecords.List(ctx, records, ref.Tier, ref.Project)
	if err != nil {
		return err
	}
	for _, stack := range recorded {
		if stack.Name != ref.Name && !stack.Name.IsInfra() && stack.Name.App == ref.Name.App {
			return nil
		}
	}
	member := "serviceAccount:" + c.AppAccountEmail(ref.Tier, ref.Project, ref.Name.App)
	if err := c.unbindProjectMember(ctx, member); err != nil {
		return err
	}
	if err := absentIsDone(c.bindQueueRoles(ctx, ref.Tier, member, nil)); err != nil {
		return err
	}
	if err := absentIsDone(c.bindAccountRole(ctx, c.DelayAccount(ref.Tier), runAsRole, member, false)); err != nil {
		return err
	}
	if err := c.revokeTopicPublisher(ctx, recorded, ref, member); err != nil {
		return err
	}
	if progress != nil {
		progress.Say(fmt.Sprintf("Revoked what app %s of %s was granted in the %s tier: no environment runs it", ref.Name.App, ref.Project, ref.Tier))
	}
	return nil
}

func (c *clients) revokeTopicPublisher(ctx context.Context, recorded []stackrecords.NamedStack, ref provider.StackRef, member string) error {
	for _, stack := range recorded {
		if !stack.Name.IsInfra() {
			continue
		}
		declared := map[string]*provider.TopicSpec{}
		for _, binding := range stack.Bindings {
			topic, isTopic, err := readDeclaredTopic(binding)
			if err != nil {
				return err
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
		if _, err := topology.RevokePublisher(ctx, c.Workload()); err != nil {
			return err
		}
	}
	return nil
}

func absentIsDone(err error) error {
	if err != nil && (absent(err) || status.Code(err) == codes.NotFound) {
		return nil
	}
	return err
}
