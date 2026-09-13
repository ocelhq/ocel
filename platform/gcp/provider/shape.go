package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/costkit"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

const (
	scopeProject     = "project"
	scopeShared      = "shared"
	scopeEnvironment = "environment"
	scopeApp         = "app"

	tfCloudRunService     = "google_cloud_run_v2_service"
	tfFirestoreDatabase   = "google_firestore_database"
	tfStorageBucket       = "google_storage_bucket"
	tfKMSKeyRing          = "google_kms_key_ring"
	tfKMSCryptoKey        = "google_kms_crypto_key"
	tfServiceAccount      = "google_service_account"
	tfArtifactRepository  = "google_artifact_registry_repository"
	tfSecretManagerSecret = "google_secret_manager_secret"

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
}

func (p *Provider) Shape(_ context.Context, req providerkit.ShapeRequest) (*costv1.ResourceSet, error) {
	tree := &costkit.Tree{}
	region := p.options.Region
	project := tree.Scope("", scopeProject, req.Plan.Slug)
	shared := tree.Scope(project, scopeShared, string(req.Plan.Class))
	environment := tree.Scope(project, scopeEnvironment, req.Plan.Env)

	for _, item := range bootstrapItems(p.Names(), req.Plan.Class, false) {
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
	fronted := req.Edge == alb.Kind
	if fronted {
		previewBase := ""
		if req.Plan.Class == providerkit.ClassPreview {
			previewBase = "preview"
		}
		for _, item := range alb.ShapeFront(req.Plan.Class, previewBase) {
			tree.Add(shared, string(Vendor), item.Type, item.Name, region, item.Properties)
		}
	}

	for _, app := range req.Plan.Apps {
		scope := tree.Scope(environment, scopeApp, app.App)
		if app.Compute() == providerkit.ComputeContainer {
			service, err := p.Names().Service(req.Plan.Slug, req.Plan.Env, app.App, app.App)
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
				service, err := p.Names().Service(req.Plan.Slug, req.Plan.Env, app.App, spec.Name)
				if err != nil {
					return nil, err
				}
				tree.Add(scope, string(Vendor), tfCloudRunService, service, region, serviceProperties(providerkit.ComputeServerless, ingress))
			}
		}
		if fronted {
			for _, item := range alb.ShapeHosts(req.Plan.Slug, req.Plan.Class, productionHostnames(app)) {
				tree.Add(scope, string(Vendor), item.Type, item.Name, region, item.Properties)
			}
		}
	}
	return tree.Set(providerkit.CostSource), nil
}

func productionHostnames(app providerkit.AppEntry) []string {
	for _, domains := range app.Manifest.GetDomains() {
		if domains.GetTier() == environmentv1.Tier_TIER_PRODUCTION {
			return domains.GetHostnames()
		}
	}
	return nil
}

func itemProperties(item item, region string) map[string]any {
	switch item.Kind {
	case KindBucket:
		return map[string]any{"location": region, "storage_class": "STANDARD", "versioning": map[string]any{"enabled": item.Versioned}}
	case KindRepository:
		return map[string]any{"location": region, "format": "DOCKER"}
	case KindSecret:
		return map[string]any{"replication": map[string]any{"auto": map[string]any{}}}
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
	return cost.Price(req)
}
