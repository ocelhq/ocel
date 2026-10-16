package gcp

import (
	"context"
	"slices"
	"strconv"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/pricing"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	cloudflarecost "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy/cost"
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
	tfProjectCustomRole   = "google_project_iam_custom_role"
	tfArtifactRepository  = "google_artifact_registry_repository"
	tfSecretManagerSecret = "google_secret_manager_secret"
	tfSchedulerJob        = "google_cloud_scheduler_job"
	tfMemorystoreInstance = "google_memorystore_instance"
	tfSQLInstance         = "google_sql_database_instance"
	tfNetwork             = "google_compute_network"
	tfSubnetwork          = "google_compute_subnetwork"
	tfConnectionPolicy    = "google_network_connectivity_service_connection_policy"
	tfPubSubTopic         = "google_pubsub_topic"
	tfPubSubSubscription  = "google_pubsub_subscription"
	tfCloudTasksQueue     = "google_cloud_tasks_queue"
	tfProjectIAMMember    = "google_project_iam_member"
)

var itemTypes = map[Kind]string{
	KindDatabase:       tfFirestoreDatabase,
	KindBucket:         tfStorageBucket,
	KindKeyRing:        tfKMSKeyRing,
	KindKey:            tfKMSCryptoKey,
	KindServiceAccount: tfServiceAccount,
	KindRole:           tfProjectCustomRole,
	KindRepository:     tfArtifactRepository,
	KindSecret:         tfSecretManagerSecret,
	KindService:        tfCloudRunService,
	KindSchedule:       tfSchedulerJob,
	KindGrant:          tfProjectIAMMember,
	KindQueue:          tfCloudTasksQueue,
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
		if item.Kind == KindQueue {
			continue
		}
		typ, priced := itemTypes[item.Kind]
		if !priced {
			return nil, refusal.Refuse(refusal.CodeInvalid, "bootstrap item %s has no shape", item.ID())
		}
		tree.Add(shared, string(Vendor), typ, item.Name, region, itemProperties(item, region))
	}
	tree.AddShaped(shared, string(Vendor), region, syncShape(names.EnvSourceSync(req.Deploy.Tier), region))
	if slices.ContainsFunc(req.Deploy.Apps, func(app provider.AppEntry) bool {
		return app.Manifest.GetFramework().GetName() == buildoutput.FrameworkNext
	}) {
		tree.AddShaped(shared, string(Vendor), region, tagDatabaseShape(names, req.Deploy.Tier, region))
	}

	front, err := p.Edges().Open(req.Edge, nil)
	if err != nil {
		return nil, err
	}
	ingress := ingressFor(factsOf(front))
	site := pricing.EdgeSite{Slug: req.Deploy.Slug, Tier: req.Deploy.Tier, Region: region}
	for _, app := range req.Deploy.Apps {
		site.Apps = append(site.Apps, pricing.EdgeApp{Name: app.App, Hostnames: provider.ProductionHostnames(app)})
	}
	shapes, err := edgeShapes(front.Kind(), string(p.namespace), site)
	if err != nil {
		return nil, err
	}
	for _, shape := range shapes {
		tree.AddShaped(shared, shape.Vendor, shape.Region, shape.Shared)
		tree.AddShaped(environment, shape.Vendor, shape.Region, shape.Environment)
	}

	for _, app := range req.Deploy.Apps {
		scope := tree.Scope(environment, pricing.ScopeApp, app.App)
		memory := revisionMemory
		if app.Manifest.GetFramework().GetName() == buildoutput.FrameworkNext {
			memory = formatMemoryLimit(nextMemoryMB)
		}
		if app.Compute() == provider.ComputeContainer {
			service, err := names.Service(req.Deploy.Slug, req.Deploy.Env, app.App, app.App)
			if err != nil {
				return nil, err
			}
			tree.Add(scope, string(Vendor), tfCloudRunService, service, region, serviceProperties(false, app.Instances.Min, memory, ingress))
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
				tree.Add(scope, string(Vendor), tfCloudRunService, service, region, serviceProperties(app.Manifest.GetFramework().GetName() != buildoutput.FrameworkNext, 0, memory, ingress))
			}
		}
		for _, shape := range shapes {
			tree.AddShaped(scope, shape.Vendor, shape.Region, shape.Apps[app.App])
		}
		for _, worker := range app.Workers {
			service := names.WorkerService(req.Deploy.Slug, req.Deploy.Env, app.App, worker.Name)
			tree.Add(scope, string(Vendor), tfCloudRunService, service, region, serviceProperties(true, 0, revisionMemory, ingressInternal))
		}
	}
	shapeBuckets(tree, names, req, region, environment)
	if err := shapeStores(tree, names, req, region, shared, environment); err != nil {
		return nil, err
	}
	shapeTopics(tree, names, req, region, shared, environment)
	shapeRealtime(tree, names, req, region, environment)
	return tree.Set(provider.CostSource)
}

func shapeBuckets(tree *pricing.Tree, names Names, req provider.ShapeRequest, region, environment string) {
	for _, resource := range req.Resources {
		if resource.Type != provider.BindingBucket {
			continue
		}
		bucket := names.AppBucket(req.Deploy.Slug, req.Deploy.Env, resource.Name)
		tree.Add(environment, string(Vendor), tfStorageBucket, bucket, region, itemProperties(item{Kind: KindBucket}, region))
	}
}

func shapeStores(tree *pricing.Tree, names Names, req provider.ShapeRequest, region, shared, environment string) error {
	declared := false
	for _, resource := range req.Resources {
		switch resource.Type {
		case provider.BindingKV:
			store, err := readKVStore(resource)
			if err != nil {
				return err
			}
			instance, err := names.KVInstance(req.Deploy.Slug, req.Deploy.Env, resource.Name)
			if err != nil {
				return err
			}
			tree.Add(environment, string(Vendor), tfMemorystoreInstance, instance, region, store.shapeProperties(region))
		case provider.BindingPostgres:
			spec, err := readPostgres(resource)
			if err != nil {
				return err
			}
			instance := names.PostgresInstance(req.Deploy.Slug, req.Deploy.Env, resource.Name)
			tree.Add(environment, string(Vendor), tfSQLInstance, instance, region, spec.shapeProperties(region))
		default:
			continue
		}
		declared = true
	}
	if !declared {
		return nil
	}
	tree.AddShaped(shared, string(Vendor), region, networkShape(names, req.Deploy.Tier, region))
	return nil
}

func shapeTopics(tree *pricing.Tree, names Names, req provider.ShapeRequest, region, shared, environment string) {
	scope := taskNames(names, provider.StackRef{Project: req.Deploy.Slug, Tier: req.Deploy.Tier, Name: naming.InfraStack(req.Deploy.Env)})
	declared := false
	for _, resource := range req.Resources {
		if resource.Type != provider.BindingTopic && resource.Type != provider.BindingTask {
			continue
		}
		declared = true
		topic := declaredTopicOf(resource)
		tree.Add(environment, string(Vendor), tfPubSubTopic, scope.Topic(topic.declared), region, map[string]any{})
		for _, consumer := range topic.spec.Consumers {
			tree.Add(environment, string(Vendor), tfPubSubSubscription, scope.Subscription(topic.declared, consumer.Name), region,
				map[string]any{"ack_deadline_seconds": int(pushCeiling.Seconds()), "enable_message_ordering": topic.spec.Ordered})
			tree.Add(environment, string(Vendor), tfPubSubTopic, scope.DeadLetterTopic(topic.declared, consumer.Name), region, map[string]any{"dead_letter": true})
		}
		if topic.spec.Cron != "" {
			tree.Add(environment, string(Vendor), tfSchedulerJob, scope.ScheduleJob(topic.declared), region, map[string]any{"region": region, "schedule": topic.spec.Cron})
		}
	}
	if !declared {
		return
	}
	tree.AddShaped(shared, string(Vendor), region, tasksShape(names, req.Deploy.Tier, region))
	queue := item{Kind: KindQueue, Name: names.DelayQueue(req.Deploy.Tier)}
	tree.Add(shared, string(Vendor), itemTypes[KindQueue], queue.Name, region, itemProperties(queue, region))
}

func shapeRealtime(tree *pricing.Tree, names Names, req provider.ShapeRequest, region, environment string) {
	secret := itemProperties(item{Kind: KindSecret}, region)
	declared := false
	for _, resource := range req.Resources {
		if resource.Type != provider.BindingRealtime {
			continue
		}
		declared = true
		namespace := realtimeNamespaceOf(resource.Declared, resource.Name)
		tree.Add(environment, string(Vendor), tfSecretManagerSecret, names.RealtimeSigningSecret(req.Deploy.Slug, req.Deploy.Env, namespace), region, secret)
	}
	if !declared {
		return
	}
	tree.Add(environment, string(Vendor), tfSecretManagerSecret, names.RealtimeKeysSecret(req.Deploy.Tier, req.Deploy.Slug, req.Deploy.Env), region, secret)
	tree.Add(environment, string(Vendor), tfCloudRunService, names.RealtimeGateway(req.Deploy.Slug, req.Deploy.Env), region, map[string]any{
		"location":      region,
		"ingress":       ingressEverywhere,
		"holds_sockets": true,
		"template": map[string]any{
			"scaling":                          map[string]any{"min_instance_count": 0, "max_instance_count": realtimeGatewayInstances},
			"max_instance_request_concurrency": realtimeGatewayConcurrency,
			"containers": []any{map[string]any{
				"resources": map[string]any{
					"cpu_idle": true,
					"limits":   map[string]any{"cpu": realtimeGatewayCPU, "memory": strconv.Itoa(realtimeGatewayMemoryMiB) + "Mi"},
				},
			}},
		},
	})
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
	case KindQueue:
		return map[string]any{"location": region}
	}
	return map[string]any{}
}

func serviceProperties(billsPerRequest bool, minInstances int, memory, ingress string) map[string]any {
	return map[string]any{
		"ingress": ingress,
		"template": map[string]any{
			"scaling": map[string]any{"min_instance_count": minInstances},
			"containers": []any{map[string]any{
				"resources": map[string]any{
					"cpu_idle": billsPerRequest,
					"limits":   map[string]any{"cpu": revisionCPU, "memory": memory},
				},
			}},
		},
	}
}

func (p *Provider) EstimateCost(_ context.Context, req *costv1.PriceRequest) (*costv1.Estimate, error) {
	return cost.Price(req, cloudflarecost.Rates)
}

func edgeShapes(kind edge.Kind, namespace string, site pricing.EdgeSite) ([]pricing.EdgeShape, error) {
	if kind != alb.Kind && kind != cloudflare.Kind {
		return nil, nil
	}
	balancer, err := alb.Shape(site)
	if err != nil || kind == alb.Kind {
		return []pricing.EdgeShape{balancer}, err
	}
	worker, err := cloudflare.Shape(namespace, site)
	if err != nil {
		return nil, err
	}
	return []pricing.EdgeShape{balancer, worker}, nil
}
