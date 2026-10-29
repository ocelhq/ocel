package gcp_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/artifactregistry"
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/certificatemanager"
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/cloudrunv2"
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/cloudscheduler"
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/cloudtasks"
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/compute"
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/firestore"
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/kms"
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/logging"
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/memorystore"
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/networkconnectivity"
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/networkservices"
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/projects"
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/pubsub"
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/secretmanager"
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/serviceaccount"
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/sql"
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/storage"
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/tags"
	"google.golang.org/protobuf/encoding/protojson"

	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
)

var terraformArguments = map[string]any{
	"google_artifact_registry_repository":                   artifactregistry.RepositoryArgs{},
	"google_certificate_manager_certificate_map":            certificatemanager.CertificateMapResourceArgs{},
	"google_certificate_manager_certificate_map_entry":      certificatemanager.CertificateMapEntryArgs{},
	"google_cloud_run_v2_service":                           cloudrunv2.ServiceArgs{},
	"google_cloud_scheduler_job":                            cloudscheduler.JobArgs{},
	"google_cloud_tasks_queue":                              cloudtasks.QueueArgs{},
	"google_compute_backend_bucket":                         compute.BackendBucketArgs{},
	"google_compute_backend_service":                        compute.BackendServiceArgs{},
	"google_compute_global_address":                         compute.GlobalAddressArgs{},
	"google_compute_global_forwarding_rule":                 compute.GlobalForwardingRuleArgs{},
	"google_compute_network":                                compute.NetworkArgs{},
	"google_compute_region_network_endpoint_group":          compute.RegionNetworkEndpointGroupArgs{},
	"google_compute_subnetwork":                             compute.SubnetworkArgs{},
	"google_compute_target_https_proxy":                     compute.TargetHttpsProxyArgs{},
	"google_compute_url_map":                                compute.URLMapArgs{},
	"google_firestore_database":                             firestore.DatabaseArgs{},
	"google_kms_crypto_key":                                 kms.CryptoKeyArgs{},
	"google_kms_key_ring":                                   kms.KeyRingArgs{},
	"google_logging_log_view":                               logging.LogViewArgs{},
	"google_memorystore_instance":                           memorystore.InstanceArgs{},
	"google_network_connectivity_service_connection_policy": networkconnectivity.ServiceConnectionPolicyArgs{},
	"google_network_services_lb_edge_extension":             networkservices.LbEdgeExtensionArgs{},
	"google_network_services_wasm_plugin":                   networkservices.WasmPluginArgs{},
	"google_project_iam_custom_role":                        projects.IAMCustomRoleArgs{},
	"google_project_iam_member":                             projects.IAMMemberArgs{},
	"google_project_service_identity":                       projects.ServiceIdentityArgs{},
	"google_pubsub_subscription":                            pubsub.SubscriptionArgs{},
	"google_pubsub_topic":                                   pubsub.TopicArgs{},
	"google_secret_manager_secret":                          secretmanager.SecretArgs{},
	"google_service_account":                                serviceaccount.AccountArgs{},
	"google_sql_database_instance":                          sql.DatabaseInstanceArgs{},
	"google_storage_bucket":                                 storage.BucketArgs{},
	"google_tags_tag_key":                                   tags.TagKeyArgs{},
}

var pulumiAcronyms = map[string]string{"url": "URL", "iam": "IAM"}

func pulumiField(argument string) string {
	var b strings.Builder
	for word := range strings.SplitSeq(argument, "_") {
		if acronym, ok := pulumiAcronyms[word]; ok {
			b.WriteString(acronym)
			continue
		}
		b.WriteString(strings.ToUpper(word[:1]) + word[1:])
	}
	return b.String()
}

func refuseInventedArguments(t *testing.T, set *costv1.ResourceSet) {
	t.Helper()
	for _, listed := range set.GetResources() {
		if listed.GetVendor() != "gcp" {
			continue
		}
		args, known := terraformArguments[listed.GetType()]
		if !known {
			t.Errorf("%s is a type this test holds no Terraform arguments for", listed.GetType())
			continue
		}
		schema := reflect.TypeOf(args)
		arguments := append([]string{}, listed.GetUnknown()...)
		for key := range listed.GetProperties().GetFields() {
			arguments = append(arguments, key)
		}
		for key := range listed.GetReferences() {
			arguments = append(arguments, key)
		}
		for _, argument := range arguments {
			top, _, _ := strings.Cut(argument, ".")
			if _, ok := schema.FieldByName(pulumiField(top)); !ok {
				t.Errorf("%s lists %q, which is no argument of the Terraform resource", listed.GetId(), argument)
			}
		}
	}
}

func TestEveryInventoryGoldenListsOnlyTerraformArguments(t *testing.T) {
	goldens, err := filepath.Glob(filepath.Join("testdata", "inventory_*.golden.json"))
	if err != nil || len(goldens) == 0 {
		t.Fatalf("no inventory goldens: %v", err)
	}
	for _, path := range goldens {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		set := &costv1.ResourceSet{}
		if err := protojson.Unmarshal(raw, set); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		refuseInventedArguments(t, set)
	}
}

func TestEveryDeclaredResourceIsListedWithOnlyTerraformArguments(t *testing.T) {
	refuseInventedArguments(t, inventoryOfADatabase(t))
	refuseInventedArguments(t, inventoryOfAStore(t, productionEnvironment, "2gb"))
	for _, manifest := range []*contractv1.Manifest{topicsManifest(), realtimeManifest()} {
		set, err := inventoryServed(t).Inventory(context.Background(), &contractv1.InventoryRequest{
			Manifest:    manifest,
			Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
		})
		if err != nil {
			t.Fatalf("Inventory() = %v", err)
		}
		refuseInventedArguments(t, set)
	}
}

func TestADatabaseIsListedReachedOverPrivateServiceConnect(t *testing.T) {
	properties := resourceOfType(t, inventoryOfADatabase(t), "google_sql_database_instance").GetProperties().AsMap()

	settings, _ := properties["settings"].(map[string]any)
	ip, _ := settings["ip_configuration"].(map[string]any)
	psc, _ := ip["psc_config"].(map[string]any)
	if psc["psc_enabled"] != true {
		t.Errorf("the database is listed with settings %v, want psc_config.psc_enabled, the endpoint Ocel reaches it through", settings)
	}
}

func TestARealtimeGatewayIsListedWithTheTimeoutAndConcurrencyItRunsWith(t *testing.T) {
	set, err := inventoryServed(t).Inventory(context.Background(), &contractv1.InventoryRequest{
		Manifest:    realtimeManifest(),
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		t.Fatalf("Inventory() = %v", err)
	}
	gateway := gatewayOf(t, namesOf(t, newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"})), "shop", "prod")
	for _, r := range set.GetResources() {
		if r.GetType() != "google_cloud_run_v2_service" || r.GetName() != gateway {
			continue
		}
		template := r.GetProperties().AsMap()["template"].(map[string]any)
		if template["timeout"] != "3600s" || template["max_instance_request_concurrency"] != float64(1000) {
			t.Errorf("the gateway is listed with template %v, want the 3600s timeout and 1000 requests an instance serves", template)
		}
		return
	}
	t.Fatalf("no realtime gateway among %v", set.GetResources())
}
