package alb

import (
	"maps"
	"slices"

	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/certificatemanager"
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/compute"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type bindingSpec struct {
	Project        string
	Region         string
	Slug           string
	Class          edge.Class
	CertificateMap string
	Hosts          map[string]Host
}

func bindingProgram(spec bindingSpec) Program {
	return func(ctx *pulumi.Context) error {
		project := pulumi.String(spec.Project)
		for _, hostname := range slices.Sorted(maps.Keys(spec.Hosts)) {
			host := spec.Hosts[hostname]
			if host.Certificate != "" {
				entry := entryName(spec.Slug, spec.Class, hostname)
				if _, err := certificatemanager.NewCertificateMapEntry(ctx, entry,
					&certificatemanager.CertificateMapEntryArgs{
						Name:         pulumi.String(entry),
						Project:      project,
						Map:          pulumi.String(spec.CertificateMap),
						Hostname:     pulumi.String(hostname),
						Certificates: pulumi.StringArray{pulumi.String(host.Certificate)},
					}); err != nil {
					return err
				}
			}
			if host.Service == "" {
				continue
			}
			neg := negName(spec.Slug, spec.Class, hostname)
			group, err := compute.NewRegionNetworkEndpointGroup(ctx, neg, &compute.RegionNetworkEndpointGroupArgs{
				Name:                pulumi.String(neg),
				Project:             project,
				Region:              pulumi.String(spec.Region),
				NetworkEndpointType: pulumi.String(serverlessNEG),
				CloudRun: &compute.RegionNetworkEndpointGroupCloudRunArgs{
					Service: pulumi.String(host.Service),
				},
			})
			if err != nil {
				return err
			}
			if _, err := compute.NewBackendService(ctx, host.Backend, &compute.BackendServiceArgs{
				Name:                pulumi.String(host.Backend),
				Project:             project,
				Protocol:            pulumi.String("HTTPS"),
				LoadBalancingScheme: pulumi.String(externalManaged),
				EnableCdn:           pulumi.Bool(true),
				CdnPolicy: &compute.BackendServiceCdnPolicyArgs{
					CacheMode: pulumi.String(cacheOnOrigin),
				},
				Backends: compute.BackendServiceBackendArray{
					&compute.BackendServiceBackendArgs{Group: group.SelfLink},
				},
			}); err != nil {
				return err
			}
		}
		return nil
	}
}
