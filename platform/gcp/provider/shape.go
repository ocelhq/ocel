package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/costkit"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
)

const (
	tfCloudRunService     = "google_cloud_run_v2_service"
	tfFirestoreDatabase   = "google_firestore_database"
	tfStorageBucket       = "google_storage_bucket"
	tfKMSKeyRing          = "google_kms_key_ring"
	tfKMSCryptoKey        = "google_kms_crypto_key"
	tfServiceAccount      = "google_service_account"
	tfArtifactRepository  = "google_artifact_registry_repository"
	tfSecretManagerSecret = "google_secret_manager_secret"
	tfSchedulerJob        = "google_cloud_scheduler_job"

	revisionMinInstancesContainer  = 1
	revisionMinInstancesServerless = 0
)

var itemTypes = map[Kind]string{
	KindDatabase:       tfFirestoreDatabase,
	KindBucket:         tfStorageBucket,
	KindKeyRing:        tfKMSKeyRing,
	KindKey:            tfKMSCryptoKey,
	KindServiceAccount: tfServiceAccount,
	KindRepository:     tfArtifactRepository,
	KindSecret:         tfSecretManagerSecret,
	KindService:        tfCloudRunService,
	KindSchedule:       tfSchedulerJob,
}

func (p *Provider) Shape(_ context.Context, req providerkit.ShapeRequest) (*costv1.ResourceSet, error) {
	names, err := p.named()
	if err != nil {
		return nil, err
	}
	tree := &costkit.Tree{}
	region := p.options.Region
	project := tree.Scope("", costkit.ScopeProject, req.Plan.Slug)
	shared := tree.Scope(project, costkit.ScopeShared, string(req.Plan.Class))
	environment := tree.Scope(project, costkit.ScopeEnvironment, req.Plan.Env)

	for _, item := range bootstrapItems(names, req.Plan.Class, false) {
		typ, priced := itemTypes[item.Kind]
		if !priced {
			return nil, providerkit.Refuse(providerkit.CodeInvalid, "bootstrap item %s has no shape", item.ID())
		}
		tree.Add(shared, string(Vendor), typ, item.Name, region, itemProperties(item, region))
	}

	front, err := p.Edges().Open(req.Edge)
	if err != nil {
		return nil, err
	}
	ingress := ingressFor(factsOf(front))
	site := costkit.EdgeSite{Slug: req.Plan.Slug, Class: req.Plan.Class, Region: region}
	for _, app := range req.Plan.Apps {
		site.Apps = append(site.Apps, costkit.EdgeApp{Name: app.App, Hostnames: providerkit.ProductionHostnames(app)})
	}
	shape, err := costkit.ShapeEdge(front, site)
	if err != nil {
		return nil, err
	}
	tree.AddShaped(shared, shape.Vendor, shape.Region, shape.Shared)
	tree.AddShaped(environment, shape.Vendor, shape.Region, shape.Environment)

	for _, app := range req.Plan.Apps {
		scope := tree.Scope(environment, costkit.ScopeApp, app.App)
		if app.Compute() == providerkit.ComputeContainer {
			service, err := names.Service(req.Plan.Slug, req.Plan.Env, app.App, app.App)
			if err != nil {
				return nil, err
			}
			tree.Add(scope, string(Vendor), tfCloudRunService, service, region, serviceProperties(providerkit.ComputeContainer, ingress))
		} else {
			specs := req.Functions[app.App]
			if len(specs) == 0 {
				specs = []providerkit.FunctionSpec{{Name: app.App}}
			}
			for _, spec := range specs {
				service, err := names.Service(req.Plan.Slug, req.Plan.Env, app.App, spec.Name)
				if err != nil {
					return nil, err
				}
				tree.Add(scope, string(Vendor), tfCloudRunService, service, region, serviceProperties(providerkit.ComputeServerless, ingress))
			}
		}
		tree.AddShaped(scope, shape.Vendor, shape.Region, shape.Apps[app.App])
	}
	return tree.Set(providerkit.CostSource)
}

func itemProperties(item item, region string) map[string]any {
	switch item.Kind {
	case KindBucket:
		return map[string]any{"location": region, "storage_class": "STANDARD", "versioning": map[string]any{"enabled": item.Versioned}}
	case KindRepository:
		return map[string]any{"location": region, "format": "DOCKER"}
	case KindSecret:
		return map[string]any{"replication": map[string]any{"auto": map[string]any{}}}
	case KindService:
		return map[string]any{
			"location":                   region,
			"ingress":                    envSyncIngress,
			"requests_per_hour":          envSyncRequestsPerHour,
			"billed_seconds_per_request": envSyncBilledSecondsEach,
			"template": map[string]any{
				"scaling": map[string]any{"min_instance_count": 0, "max_instance_count": envSyncInstances},
				"containers": []any{map[string]any{
					"resources": map[string]any{
						"cpu_idle": true,
						"limits":   map[string]any{"cpu": envSyncCPU, "memory": envSyncMemory},
					},
				}},
			},
		}
	case KindSchedule:
		return map[string]any{"region": region, "schedule": envSyncSchedule}
	}
	return map[string]any{}
}

func serviceProperties(compute providerkit.Compute, ingress string) map[string]any {
	minInstances := revisionMinInstancesServerless
	if compute == providerkit.ComputeContainer {
		minInstances = revisionMinInstancesContainer
	}
	return map[string]any{
		"ingress": ingress,
		"template": map[string]any{
			"scaling": map[string]any{"min_instance_count": minInstances},
			"containers": []any{map[string]any{
				"resources": map[string]any{
					"cpu_idle": compute == providerkit.ComputeServerless,
					"limits":   map[string]any{"cpu": revisionCPU, "memory": revisionMemory},
				},
			}},
		},
	}
}

func (p *Provider) Price(_ context.Context, req *costv1.PriceRequest) (*costv1.Estimate, error) {
	edges, err := providerkit.EdgePricers(p.Edges())
	if err != nil {
		return nil, err
	}
	return cost.Price(req, edges...)
}
