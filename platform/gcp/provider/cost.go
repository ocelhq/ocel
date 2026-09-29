package gcp

import (
	"context"
	"strconv"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/pricing"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
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

func (p *Provider) ShapeCost(_ context.Context, req provider.ShapeRequest) (*costv1.ResourceSet, error) {
	names, err := p.named()
	if err != nil {
		return nil, err
	}
	tree := &pricing.Tree{}
	region := p.options.Region
	project := tree.Scope("", pricing.ScopeProject, req.Deploy.Slug)
	shared := tree.Scope(project, pricing.ScopeShared, string(req.Deploy.Tier))
	environment := tree.Scope(project, pricing.ScopeEnvironment, req.Deploy.Env)

	for _, item := range bootstrapItems(names, req.Deploy.Tier, false) {
		typ, priced := itemTypes[item.Kind]
		if !priced {
			return nil, refusal.Refuse(refusal.CodeInvalid, "bootstrap item %s has no shape", item.ID())
		}
		tree.Add(shared, string(Vendor), typ, item.Name, region, itemProperties(item, region))
	}

	front, err := p.Edges().Open(req.Edge)
	if err != nil {
		return nil, err
	}
	ingress := ingressFor(factsOf(front))
	site := pricing.EdgeSite{Slug: req.Deploy.Slug, Tier: req.Deploy.Tier, Region: region}
	for _, app := range req.Deploy.Apps {
		site.Apps = append(site.Apps, pricing.EdgeApp{Name: app.App, Hostnames: provider.ProductionHostnames(app)})
	}
	shape, err := edgeShape(front.Kind(), site)
	if err != nil {
		return nil, err
	}
	tree.AddShaped(shared, shape.Vendor, shape.Region, shape.Shared)
	tree.AddShaped(environment, shape.Vendor, shape.Region, shape.Environment)

	for _, app := range req.Deploy.Apps {
		scope := tree.Scope(environment, pricing.ScopeApp, app.App)
		if app.Compute() == provider.ComputeContainer {
			service, err := names.Service(req.Deploy.Slug, req.Deploy.Env, app.App, app.App)
			if err != nil {
				return nil, err
			}
			tree.Add(scope, string(Vendor), tfCloudRunService, service, region, serviceProperties(provider.ComputeContainer, ingress))
		} else {
			specs := req.Functions[app.App]
			if len(specs) == 0 {
				specs = []provider.FunctionSpec{{Name: app.App}}
			}
			for _, spec := range specs {
				service, err := names.Service(req.Deploy.Slug, req.Deploy.Env, app.App, spec.Name)
				if err != nil {
					return nil, err
				}
				tree.Add(scope, string(Vendor), tfCloudRunService, service, region, serviceProperties(provider.ComputeServerless, ingress))
			}
		}
		tree.AddShaped(scope, shape.Vendor, shape.Region, shape.Apps[app.App])
	}
	return tree.Set(provider.CostSource)
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
			"ingress":                    ingressInternal,
			"requests_per_hour":          syncRequestsPerHour,
			"billed_seconds_per_request": syncBilledSecondsEach,
			"template": map[string]any{
				"scaling": map[string]any{"min_instance_count": 0, "max_instance_count": syncInstances},
				"containers": []any{map[string]any{
					"resources": map[string]any{
						"cpu_idle": true,
						"limits":   map[string]any{"cpu": syncCPU, "memory": strconv.Itoa(syncMemoryMiB) + "Mi"},
					},
				}},
			},
		}
	case KindSchedule:
		return map[string]any{"region": region, "schedule": syncSchedule}
	}
	return map[string]any{}
}

func serviceProperties(compute provider.Compute, ingress string) map[string]any {
	minInstances := revisionMinInstancesServerless
	if compute == provider.ComputeContainer {
		minInstances = revisionMinInstancesContainer
	}
	return map[string]any{
		"ingress": ingress,
		"template": map[string]any{
			"scaling": map[string]any{"min_instance_count": minInstances},
			"containers": []any{map[string]any{
				"resources": map[string]any{
					"cpu_idle": compute == provider.ComputeServerless,
					"limits":   map[string]any{"cpu": revisionCPU, "memory": revisionMemory},
				},
			}},
		},
	}
}

func (p *Provider) EstimateCost(_ context.Context, req *costv1.PriceRequest) (*costv1.Estimate, error) {
	return cost.Price(req)
}

func edgeShape(kind edge.Kind, site pricing.EdgeSite) (pricing.EdgeShape, error) {
	if kind != alb.Kind && kind != cloudflare.Kind {
		return pricing.EdgeShape{}, nil
	}
	return alb.Shape(site)
}
