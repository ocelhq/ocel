package alb

import (
	"maps"
	"slices"

	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/certificatemanager"
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/compute"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
)

type bindingSpec struct {
	Region         string
	Slug           string
	Tier           environment.Tier
	CertificateMap string
	Hosts          map[string]Host
	Shielded       bool
}

func bindingProgram(spec bindingSpec) Program {
	return func(ctx *pulumi.Context, in string) error {
		project := pulumi.String(in)
		for _, hostname := range slices.Sorted(maps.Keys(spec.Hosts)) {
			host := spec.Hosts[hostname]
			if host.Certificate != "" {
				entry := entryName(spec.Slug, spec.Tier, hostname)
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
			neg := negName(spec.Slug, spec.Tier, hostname)
			if err := servingBackend(ctx, project, spec.Region, neg, host, newCDNPolicy(spec.Shielded)); err != nil {
				return err
			}
		}
		return nil
	}
}

func servingBackend(ctx *pulumi.Context, project pulumi.String, region, neg string, host Host, cdn *compute.BackendServiceCdnPolicyArgs) error {
	group, err := compute.NewRegionNetworkEndpointGroup(ctx, neg, &compute.RegionNetworkEndpointGroupArgs{
		Name:                pulumi.String(neg),
		Project:             project,
		Region:              pulumi.String(region),
		NetworkEndpointType: pulumi.String(serverlessNEG),
		CloudRun: &compute.RegionNetworkEndpointGroupCloudRunArgs{
			Service: pulumi.String(host.Service),
			Tag:     pulumi.StringPtrFromPtr(omitEmpty(host.Tag)),
		},
	})
	if err != nil {
		return err
	}
	args := &compute.BackendServiceArgs{
		Name:                pulumi.String(host.Backend),
		Project:             project,
		Protocol:            pulumi.String("HTTPS"),
		LoadBalancingScheme: pulumi.String(externalManaged),
		EnableCdn:           pulumi.Bool(cdn != nil),
		Backends: compute.BackendServiceBackendArray{
			&compute.BackendServiceBackendArgs{Group: group.SelfLink},
		},
	}
	if cdn != nil {
		args.CdnPolicy = cdn
	}
	_, err = compute.NewBackendService(ctx, host.Backend, args)
	return err
}

func omitEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
