package network

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

func ReadEndpointNetwork(ctx context.Context, clients *gcloud.Clients, tier environment.Tier, binding provider.Binding) (string, error) {
	host := binding.Properties[provider.PropertyHost]
	label, err := names.LabelValue(binding.Name)
	if err != nil {
		return "", err
	}
	switch binding.Type {
	case provider.BindingKV:
		return readStoreNetwork(ctx, clients, tier, label, host)
	case provider.BindingPostgres:
		return readDatabaseNetwork(ctx, clients, tier, label, host)
	}
	return "", nil
}

func readStoreNetwork(ctx context.Context, clients *gcloud.Clients, tier environment.Tier, label, host string) (string, error) {
	stores, err := clients.Memorystore()
	if err != nil {
		return "", err
	}
	filter, err := StoresFilter(clients.Namespace(), tier)
	if err != nil {
		return "", err
	}
	listed, _, err := stores.ListInstances(ctx, clients.Location(), fmt.Sprintf("%s AND labels.%s=%q", filter, names.KVLabel, label), resourcesNamedOnNetwork, "")
	if err != nil {
		return "", fmt.Errorf("read which network kv %s is on: %w", label, err)
	}
	for _, instance := range listed {
		for _, endpoint := range instance.Endpoints {
			for _, connection := range endpoint.Connections {
				if auto := connection.PSCAutoConnection; auto != nil && auto.IPAddress == host {
					return auto.Network, nil
				}
			}
		}
	}
	return "", nil
}

func readDatabaseNetwork(ctx context.Context, clients *gcloud.Clients, tier environment.Tier, label, host string) (string, error) {
	service, err := clients.SQL()
	if err != nil {
		return "", err
	}
	filter, err := DatabasesFilter(clients.Namespace(), tier)
	if err != nil {
		return "", err
	}
	listed, err := googleretry.Attempted(ctx, service.Instances.List(clients.Project()).
		Filter(filter+" settings.userLabels."+names.PostgresLabel+":"+label).MaxResults(resourcesNamedOnNetwork).Context(ctx).Do)
	if err != nil {
		return "", fmt.Errorf("read which network postgres %s is on: %w", label, err)
	}
	for _, instance := range listed.Items {
		if instance.Settings == nil || instance.Settings.IpConfiguration == nil || instance.Settings.IpConfiguration.PscConfig == nil {
			continue
		}
		for _, auto := range instance.Settings.IpConfiguration.PscConfig.PscAutoConnections {
			if auto.IpAddress == host {
				return auto.ConsumerNetwork, nil
			}
		}
	}
	return "", nil
}

func ReadRange(ctx context.Context, clients *gcloud.Clients, tier environment.Tier) (netip.Prefix, error) {
	engine, err := clients.Compute()
	if err != nil {
		return netip.Prefix{}, err
	}
	name := clients.Subnetwork(tier)
	subnet, err := googleretry.Attempted(ctx, engine.Subnetworks.Get(clients.Project(), clients.Region, name).Context(ctx).Do)
	if gcloud.Absent(err) {
		return netip.Prefix{}, refusal.Refuse(refusal.CodeNotReady,
			"the %s tier has no %s subnetwork, which the bastion forwards ports into: deploy a kv store or database to the tier first", tier, name)
	}
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("read the %s subnetwork the bastion forwards ports into: %w", name, err)
	}
	reach, err := netip.ParsePrefix(subnet.IpCidrRange)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("read the range of the %s subnetwork: %w", name, err)
	}
	return reach, nil
}
