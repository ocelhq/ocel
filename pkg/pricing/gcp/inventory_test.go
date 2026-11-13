package gcp_test

import (
	"google.golang.org/protobuf/types/known/structpb"

	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
)

var fixtureRegions = []string{"europe-west1", "us-central1", "europe-west2"}

func resource(id, typ, region string, props map[string]any) *costv1.Resource {
	properties, err := structpb.NewStruct(props)
	if err != nil {
		panic(err)
	}
	return &costv1.Resource{Id: id, Scope: "stack", Vendor: "gcp", Type: typ, Name: id, Region: region, Properties: properties}
}

func service(cpuIdle bool, extra map[string]any) map[string]any {
	props := map[string]any{
		"ingress": "INGRESS_TRAFFIC_ALL",
		"template": map[string]any{
			"containers": []any{map[string]any{"resources": map[string]any{
				"cpu_idle": cpuIdle,
				"limits":   map[string]any{"cpu": "1", "memory": "512Mi"},
			}}},
			"scaling": map[string]any{"min_instance_count": 1},
		},
	}
	for k, v := range extra {
		props[k] = v
	}
	return props
}

func inventory(region string) *costv1.ResourceSet {
	return &costv1.ResourceSet{
		Source: "terraform",
		Scopes: []*costv1.Scope{{Id: "stack", Kind: "module", Name: "root"}},
		Resources: []*costv1.Resource{
			resource("run", "google_cloud_run_v2_service", region, service(true, nil)),
			resource("run-always", "google_cloud_run_v2_service", region, service(false, nil)),
			resource("job", "google_cloud_scheduler_job", region, map[string]any{}),
			resource("firestore", "google_firestore_database", region, map[string]any{}),
			resource("bucket", "google_storage_bucket", region, map[string]any{}),
			resource("key", "google_kms_crypto_key", region, map[string]any{}),
			resource("registry", "google_artifact_registry_repository", region, map[string]any{}),
			resource("secret", "google_secret_manager_secret", region, map[string]any{}),
			resource("backend", "google_compute_backend_service", region, map[string]any{"enable_cdn": true}),
			resource("rule", "google_compute_global_forwarding_rule", region, map[string]any{}),
			resource("store", "google_memorystore_instance", region, map[string]any{
				"node_type": "CUSTOM_PICO", "shard_count": 1, "replica_count": 1, "persistence_config": map[string]any{"mode": "AOF"},
			}),
			resource("database", "google_sql_database_instance", region, map[string]any{
				"database_version": "POSTGRES_17",
				"settings": map[string]any{"tier": "db-custom-2-7680", "edition": "ENTERPRISE", "availability_type": "REGIONAL", "disk_type": "PD_SSD", "disk_size": 10,
					"ip_configuration": []any{map[string]any{"psc_config": []any{map[string]any{"psc_enabled": true}}}}},
			}),
			resource("micro", "google_sql_database_instance", region, map[string]any{
				"database_version": "POSTGRES_17",
				"settings":         map[string]any{"tier": "db-f1-micro", "edition": "ENTERPRISE", "availability_type": "ZONAL", "disk_type": "PD_HDD", "disk_size": 10},
			}),
			resource("topic", "google_pubsub_topic", region, map[string]any{"message_retention_duration": "86400s"}),
			resource("subscription", "google_pubsub_subscription", region, map[string]any{"retain_acked_messages": true, "message_retention_duration": "604800s"}),
			resource("queue", "google_cloud_tasks_queue", region, map[string]any{}),
			resource("account", "google_service_account", region, map[string]any{}),
		},
	}
}
