package gcloud

import (
	"path"
	"slices"

	sqladmin "google.golang.org/api/sqladmin/v1"

	"github.com/ocelhq/ocel/pkg/refusal"
)

func IsOnNetwork(connected []string, network string) bool {
	named := slices.DeleteFunc(slices.Clone(connected), func(each string) bool { return each == "" })
	return len(named) == 0 || slices.Contains(named, network)
}

func (i MemorystoreInstance) Networks() []string {
	var networks []string
	for _, endpoint := range i.Endpoints {
		for _, connection := range endpoint.Connections {
			if connection.PSCAutoConnection != nil {
				networks = append(networks, connection.PSCAutoConnection.Network)
			}
		}
	}
	return networks
}

func SQLNetworks(instance *sqladmin.DatabaseInstance) []string {
	if instance.Settings == nil || instance.Settings.IpConfiguration == nil || instance.Settings.IpConfiguration.PscConfig == nil {
		return nil
	}
	var networks []string
	for _, auto := range instance.Settings.IpConfiguration.PscConfig.PscAutoConnections {
		networks = append(networks, auto.ConsumerNetwork)
	}
	return networks
}

func RefuseNetworkChange(resource, instance string, current []string, joined string) error {
	if IsOnNetwork(current, joined) {
		return nil
	}
	named := slices.DeleteFunc(slices.Clone(current), func(each string) bool { return each == "" })
	return refusal.Refuse(refusal.CodeInvalid,
		"%s runs on the %s, which is connected to the %s network, and this project joins %s: ocel moves no instance between networks.\n"+
			"Join the %s network again, or remove %s and deploy, which deletes its data, before declaring it again",
		resource, instance, path.Base(named[0]), path.Base(joined), path.Base(named[0]), resource)
}
