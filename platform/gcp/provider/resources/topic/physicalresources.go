package topic

import (
	"context"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

func (p *Topic) ListResources(_ context.Context, clients *gcloud.Clients, ref provider.StackRef, binding provider.Binding) ([]provider.PhysicalResource, error) {
	topic, found, err := readDeclaredTopic(binding)
	if err != nil || !found {
		return nil, err
	}
	scope := Names(clients.Names, ref)
	name, err := scope.Topic(topic.declared)
	if err != nil {
		return nil, err
	}
	listed := []provider.PhysicalResource{{Kind: string(bootstrap.KindTopic), ID: name, Location: bootstrap.LocationGlobal}}
	for _, consumer := range topic.spec.Consumers {
		subscription, err := scope.Subscription(topic.declared, consumer.Name)
		if err != nil {
			return nil, err
		}
		listed = append(listed, provider.PhysicalResource{Kind: string(bootstrap.KindSubscription), ID: subscription, Location: bootstrap.LocationGlobal})
	}
	if topic.spec.Cron != "" {
		listed = append(listed, provider.PhysicalResource{Kind: string(bootstrap.KindSchedule), ID: name, Location: clients.Region})
	}
	return listed, nil
}
