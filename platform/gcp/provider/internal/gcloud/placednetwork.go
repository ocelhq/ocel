package gcloud

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	sqladmin "google.golang.org/api/sqladmin/v1"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
)

const computeSelfLink = "https://www.googleapis.com/compute/v1/"

var (
	networkName = regexp.MustCompile(`^[a-z]([-a-z0-9]{0,61}[a-z0-9])?$`)
	networkPath = regexp.MustCompile(`^projects/([^/]+)/global/networks/([^/]+)$`)
)

func PlacedNetworkPath(project, written string) (string, error) {
	if networkName.MatchString(written) {
		return "projects/" + project + "/global/networks/" + written, nil
	}
	named := networkPath.FindStringSubmatch(strings.TrimPrefix(written, computeSelfLink))
	if named == nil || !networkName.MatchString(named[2]) {
		return "", fmt.Errorf("%q is neither a network name nor projects/<project>/global/networks/<name>", written)
	}
	if named[1] != project {
		return "", fmt.Errorf("%s is a network of project %s, and an endpoint here is made only in a network of %s", named[2], named[1], project)
	}
	return named[0], nil
}

func StoreNetworks(instance *MemorystoreInstance) []string {
	var networks []string
	for _, endpoint := range instance.Endpoints {
		for _, connection := range endpoint.Connections {
			if connection.PSCAutoConnection != nil {
				networks = append(networks, connection.PSCAutoConnection.Network)
			}
		}
	}
	return networks
}

func DatabaseNetworks(instance *sqladmin.DatabaseInstance) []string {
	if instance.Settings == nil || instance.Settings.IpConfiguration == nil || instance.Settings.IpConfiguration.PscConfig == nil {
		return nil
	}
	var networks []string
	for _, auto := range instance.Settings.IpConfiguration.PscConfig.PscAutoConnections {
		networks = append(networks, auto.ConsumerNetwork)
	}
	return networks
}

func CanBeOn(held []string, network string) bool {
	named := namedNetworks(held)
	return len(named) == 0 || slices.Contains(named, network)
}

func namedNetworks(held []string) []string {
	return slices.DeleteFunc(slices.Clone(held), func(network string) bool { return network == "" })
}

func RefuseEndpointMove(resource, instance string, held []string, placed string) error {
	if CanBeOn(held, placed) {
		return nil
	}
	held = namedNetworks(held)
	return refusal.Refuse(refusal.CodeInvalid,
		"%s's %s has its endpoint on the %s network, and its transforms place it on %s: an endpoint never moves between networks.\n"+
			"Place it on %s again, or remove %s and deploy, which deletes its data, before declaring it again",
		resource, instance, path.Base(held[0]), path.Base(placed), path.Base(held[0]), resource)
}

func RefuseNetworkAbsent(ctx context.Context, c *Clients, resource, network string) error {
	engine, err := c.Compute()
	if err != nil {
		return err
	}
	named := path.Base(network)
	_, err = googleretry.Attempted(ctx, engine.Networks.Get(c.Project(), named).Context(ctx).Do)
	if Absent(err) {
		return refusal.Refuse(refusal.CodeInvalid,
			"a transform places %s on the %s network, and project %s has no network by that name: make it before the deploy, or place it on one the project has",
			resource, named, c.Project())
	}
	if err != nil {
		return fmt.Errorf("read the %s network %s is placed on: %w", named, resource, err)
	}
	return nil
}
