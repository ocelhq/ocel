package gcp_test

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/pricing/gcp"
	"github.com/ocelhq/ocel/pkg/pricing/pricingtest"
	"github.com/ocelhq/ocel/pkg/pricing/source"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
)

func catalog(t *testing.T) *pricingtest.Card {
	t.Helper()
	raw, err := os.ReadFile("testdata/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	card, err := pricingtest.NewCard(raw)
	if err != nil {
		t.Fatal(err)
	}
	return card
}

func estimate(t *testing.T, set *costv1.ResourceSet) *costv1.Estimate {
	t.Helper()
	est, err := pricing.Estimate(context.Background(), catalog(t), pricing.Combine(gcp.Rules), set, nil)
	if err != nil {
		t.Fatal(err)
	}
	return est
}

func only(region string, ids ...string) *costv1.ResourceSet {
	set := inventory(region)
	set.Resources = slices.DeleteFunc(set.Resources, func(r *costv1.Resource) bool { return !slices.Contains(ids, r.GetId()) })
	return set
}

func byID(est *costv1.Estimate, id string) *costv1.ResourceEstimate {
	for _, r := range est.GetResources() {
		if r.GetResource() == id {
			return r
		}
	}
	return nil
}

func component(r *costv1.ResourceEstimate, name string) *costv1.CostComponent {
	for _, c := range r.GetComponents() {
		if c.GetName() == name {
			return c
		}
	}
	return nil
}

func names(r *costv1.ResourceEstimate) string {
	var out []string
	for _, c := range r.GetComponents() {
		out = append(out, c.GetName())
	}
	return strings.Join(out, ",")
}

func TestEveryComponentIsPricedByExactlyOneSKUInEveryFixtureRegion(t *testing.T) {
	for _, region := range fixtureRegions {
		t.Run(region, func(t *testing.T) {
			est := estimate(t, inventory(region))
			for _, r := range est.GetResources() {
				if r.GetStatus() == costv1.ResourceEstimate_STATUS_UNSUPPORTED {
					t.Errorf("%s is unsupported", r.GetResource())
				}
				for _, c := range r.GetComponents() {
					if c.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_PRICED {
						t.Errorf("%s %s = %v: %s", r.GetResource(), c.GetName(), c.GetPriceStatus(), c.GetPriceDetail())
					}
				}
			}
		})
	}
}

func TestEveryProductTheRulesAskForIsOneTheManifestIngests(t *testing.T) {
	card := catalog(t)
	for _, region := range fixtureRegions {
		if _, err := pricing.Estimate(context.Background(), card, pricing.Combine(gcp.Rules), inventory(region), nil); err != nil {
			t.Fatal(err)
		}
	}

	if missed := card.MissedBy(gcp.Rules.Ingest); len(missed) > 0 {
		t.Errorf("the manifest misses %v", missed)
	}
}

func TestARequestBilledServiceSpendsTheFreeRequestsAndPaysForCPUAndMemory(t *testing.T) {
	run := byID(estimate(t, only("europe-west1", "run")), "run")

	if names(run) != "Requests,CPU during requests,Memory during requests,CPU, idle minimum instances,Memory, idle minimum instances,Data transfer out to internet" {
		t.Fatalf("components = %s", names(run))
	}
	if requests := component(run, "Requests"); requests.GetMonthlyCost() != "0.00" {
		t.Errorf("requests = %v, want 1M inside the free 2M", requests)
	}
	if cpu := component(run, "CPU during requests"); cpu.GetMonthlyQuantity() != "200000" || cpu.GetMonthlyCost() != "4.80" {
		t.Errorf("cpu = %v, want 200,000 vCPU-seconds at 0.000024", cpu)
	}
	if idle := component(run, "CPU, idle minimum instances"); idle.GetMonthlyQuantity() != "2628000" || idle.GetUnitPrice() != "0.0000025" {
		t.Errorf("idle cpu = %v, want a whole month of one instance at the idle rate", idle)
	}
	if egress := component(run, "Data transfer out to internet"); egress.GetUnitPrice() != "0.105" {
		t.Errorf("egress = %v, want Europe to Europe", egress)
	}
}

func TestATierTwoRegionPricesCloudRunAtItsOwnRate(t *testing.T) {
	cpu := component(byID(estimate(t, only("europe-west2", "run")), "run"), "CPU during requests")

	if cpu.GetUnitPrice() != "0.0000336" {
		t.Errorf("cpu = %v, want the tier 2 rate", cpu)
	}
}

func TestAnInstanceBilledServiceIsPricedForEveryHourItsInstanceLives(t *testing.T) {
	run := byID(estimate(t, only("us-central1", "run-always")), "run-always")

	if cpu := component(run, "CPU, always allocated"); cpu.GetMonthlyQuantity() != "2628000" || cpu.GetMonthlyCost() != "47.30" {
		t.Errorf("cpu = %v, want 730 hours of one vCPU at 0.000018", cpu)
	}
}

func TestADatabaseBillsItsCoresMemoryDiskAndEndpoint(t *testing.T) {
	est := estimate(t, only("europe-west1", "database", "micro"))

	database := byID(est, "database")
	if vcpu := component(database, "vCPU"); vcpu.GetMonthlyQuantity() != "1460" || vcpu.GetUnitPrice() != "0.0826" {
		t.Errorf("vcpu = %v, want 2 vCPUs for 730 hours at the regional rate", vcpu)
	}
	if memory := component(database, "Memory"); memory.GetMonthlyQuantity() != "5475" {
		t.Errorf("memory = %v, want 7.5 GiB for 730 hours", memory)
	}
	micro := byID(est, "micro")
	if instance := component(micro, "Instance"); instance.GetUnitPrice() != "0.0105" {
		t.Errorf("micro instance = %v, want the zonal micro rate", instance)
	}
	if disk := component(micro, "Storage"); disk.GetUnitPrice() != "0.09" {
		t.Errorf("micro disk = %v, want the low cost storage rate", disk)
	}
}

func TestAStoreBillsItsNodeItsReplicaAndTheirAppendOnlyFiles(t *testing.T) {
	store := byID(estimate(t, only("us-central1", "store")), "store")

	if node := component(store, "Node"); node.GetMonthlyQuantity() != "1460" || node.GetUnitPrice() != "0.0308" {
		t.Errorf("node = %v, want two CUSTOM_PICO nodes for 730 hours", node)
	}
	if aof := component(store, "Append-only persistence"); aof.GetMonthlyQuantity() != "1825" {
		t.Errorf("aof = %v, want 1.25 GB on each of two nodes for 730 hours", aof)
	}
}

func TestATopicPaysForTheMessagesItRetains(t *testing.T) {
	set := only("europe-west1", "topic", "subscription")
	set.Resources = append(set.Resources,
		resource("silent", "google_pubsub_topic", "europe-west1", map[string]any{}),
		resource("bound", "google_pubsub_topic", "europe-west1", map[string]any{}))
	set.Resources[len(set.Resources)-1].Unknown = []string{"message_retention_duration"}
	est := estimate(t, set)

	if names(byID(est, "topic")) != "Publish throughput,Message storage" || names(byID(est, "subscription")) != "Delivery throughput,Message storage" {
		t.Errorf("topic = %s, subscription = %s", names(byID(est, "topic")), names(byID(est, "subscription")))
	}
	if names(byID(est, "silent")) != "Publish throughput" {
		t.Errorf("a topic that retains nothing = %s", names(byID(est, "silent")))
	}
	if storage := component(byID(est, "bound"), "Message storage"); storage.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_UNKNOWN_INPUT {
		t.Errorf("storage over an unknown retention = %v", storage)
	}
}

func TestAnUnknownRegionLeavesRegionNamedSKUsUnpriced(t *testing.T) {
	set := only("", "firestore", "registry")
	for _, r := range set.GetResources() {
		r.Unknown = []string{"region"}
	}
	est := estimate(t, set)

	for _, c := range byID(est, "firestore").GetComponents() {
		if c.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_UNKNOWN_INPUT {
			t.Errorf("firestore %s = %v, want it unpriced: its SKU names the location", c.GetName(), c)
		}
	}
	if storage := component(byID(est, "registry"), "Storage"); storage.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_PRICED {
		t.Errorf("registry storage = %v, want the global price", storage)
	}
}

func withLimits(cpu, memory any) map[string]any {
	props := service(true, nil)
	props["template"].(map[string]any)["containers"] = []any{map[string]any{"resources": map[string]any{
		"cpu_idle": true, "limits": map[string]any{"cpu": cpu, "memory": memory},
	}}}
	return props
}

func TestCloudRunLimitsAreReadAsKubernetesQuantities(t *testing.T) {
	set := only("europe-west1")
	set.Resources = []*costv1.Resource{
		resource("milli", "google_cloud_run_v2_service", "europe-west1", withLimits("1000m", "1Gi")),
		resource("decimal", "google_cloud_run_v2_service", "europe-west1", withLimits("2", "1G")),
		resource("garbled", "google_cloud_run_v2_service", "europe-west1", withLimits("lots", "512Q")),
	}
	est := estimate(t, set)

	if cpu := component(byID(est, "milli"), "CPU during requests"); cpu.GetMonthlyQuantity() != "200000" {
		t.Errorf("1000m cpu = %v, want one vCPU for 200,000 seconds", cpu)
	}
	if memory := component(byID(est, "milli"), "Memory during requests"); memory.GetMonthlyQuantity() != "200000" {
		t.Errorf("1Gi memory = %v, want 1 GiB for 200,000 seconds", memory)
	}
	if cpu := component(byID(est, "decimal"), "CPU during requests"); cpu.GetMonthlyQuantity() != "400000" {
		t.Errorf("2 cpu = %v, want two vCPUs for 200,000 seconds", cpu)
	}
	if memory := component(byID(est, "decimal"), "Memory during requests"); memory.GetMonthlyQuantity() != "186264.5149230957" {
		t.Errorf("1G memory = %v, want 10^9 bytes in GiB for 200,000 seconds", memory)
	}
	garbled := byID(est, "garbled")
	for name, path := range map[string]string{"CPU during requests": "template.containers.0.resources.limits.cpu", "Memory during requests": "template.containers.0.resources.limits.memory"} {
		if c := component(garbled, name); c.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_MISSING || !strings.HasPrefix(c.GetPriceDetail(), path+" is ") {
			t.Errorf("%s over an unparseable limit = %v, want it unpriced because %s cannot be read", name, c, path)
		}
	}
}

func TestEveryCloudRunServicePricesFromItsSettingsAlone(t *testing.T) {
	set := only("europe-west1", "run")
	set.Resources = append(set.Resources,
		resource("gateway", "google_cloud_run_v2_service", "europe-west1", service(true, map[string]any{"holds_sockets": true})),
		resource("sync", "google_cloud_run_v2_service", "europe-west1", service(true, map[string]any{"requests_per_hour": 12, "billed_seconds_per_request": 2})),
	)
	est := estimate(t, set)

	quantities := func(r *costv1.ResourceEstimate) string {
		var out []string
		for _, c := range r.GetComponents() {
			out = append(out, c.GetName()+"="+c.GetMonthlyQuantity())
		}
		return strings.Join(out, ",")
	}
	for _, id := range []string{"gateway", "sync"} {
		if got, want := quantities(byID(est, id)), quantities(byID(est, "run")); got != want {
			t.Errorf("%s = %s, want it priced as the plain request-based service, %s", id, got, want)
		}
	}
}

func TestAnUnknownMinimumLeavesCloudRunEgressPriced(t *testing.T) {
	run := resource("run", "google_cloud_run_v2_service", "europe-west1", service(true, nil))
	run.Unknown = []string{"template.scaling.min_instance_count"}
	set := only("europe-west1")
	set.Resources = []*costv1.Resource{run}
	est := byID(estimate(t, set), "run")

	if idle := component(est, "CPU, idle minimum instances"); idle.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_UNKNOWN_INPUT {
		t.Errorf("idle cpu = %v, want it unpriced over the unknown minimum", idle)
	}
	if egress := component(est, "Data transfer out to internet"); egress.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_PRICED {
		t.Errorf("egress = %v, want it priced: it never reads the minimum", egress)
	}
}

func TestAStoreBillsEveryNodeOfEveryShardAndItsNodeTypesAppendOnlyFile(t *testing.T) {
	set := only("us-central1")
	set.Resources = []*costv1.Resource{resource("store", "google_memorystore_instance", "us-central1", map[string]any{
		"node_type": "STANDARD_SMALL", "shard_count": 2, "replica_count": 1, "persistence_config": map[string]any{"mode": "AOF"},
	})}
	store := byID(estimate(t, set), "store")

	if node := component(store, "Node"); node.GetMonthlyQuantity() != "2920" {
		t.Errorf("node = %v, want 2 shards of 2 nodes for 730 hours", node)
	}
	if aof := component(store, "Append-only persistence"); aof.GetMonthlyQuantity() != "18980" {
		t.Errorf("aof = %v, want 6.5 GB on each of 4 nodes for 730 hours", aof)
	}
}

func TestAStoreLeavesWhatItsUnknownsReachUnpricedAndNothingElse(t *testing.T) {
	replicas := resource("replicas", "google_memorystore_instance", "us-central1", map[string]any{
		"node_type": "CUSTOM_PICO", "shard_count": 1, "persistence_config": map[string]any{"mode": "AOF"},
	})
	replicas.Unknown = []string{"replica_count"}
	mode := resource("mode", "google_memorystore_instance", "us-central1", map[string]any{"node_type": "CUSTOM_PICO", "shard_count": 1, "replica_count": 0})
	mode.Unknown = []string{"persistence_config"}
	set := only("us-central1")
	set.Resources = []*costv1.Resource{replicas, mode}
	est := estimate(t, set)

	for _, name := range []string{"Node", "Append-only persistence"} {
		if c := component(byID(est, "replicas"), name); c.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_UNKNOWN_INPUT || !slices.Contains(c.GetDependsOnUnknown(), "replica_count") {
			t.Errorf("%s = %v, want it unpriced over replica_count", name, c)
		}
	}
	if aof := component(byID(est, "mode"), "Append-only persistence"); aof.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_UNKNOWN_INPUT {
		t.Errorf("aof behind an unknown mode = %v, want it unpriced", aof)
	}
	if processed := component(byID(est, "mode"), "Inter-zone data processed"); processed.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_PRICED {
		t.Errorf("data processed = %v, want it priced: it never reads the mode", processed)
	}
}

func TestAStoreWhoseNodeTypeTheProviderChoosesIsUnpricedSayingSo(t *testing.T) {
	set := only("us-central1")
	set.Resources = []*costv1.Resource{resource("chosen", "google_memorystore_instance", "us-central1", map[string]any{
		"shard_count": 1, "persistence_config": map[string]any{"mode": "AOF"},
	})}
	store := byID(estimate(t, set), "chosen")

	for _, name := range []string{"Node", "Append-only persistence"} {
		if c := component(store, name); c.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_MISSING ||
			c.GetPriceDetail() != "node_type is not set, so the provider chooses it on apply" {
			t.Errorf("%s = %v, want it unpriced because the provider chooses node_type on apply", name, c)
		}
	}
}

func TestADatabasePlannedWithoutADiskSizeRunsTheSmallestDisk(t *testing.T) {
	plan := `{"format_version":"1.2","resource_changes":[
		{"address":"google_sql_database_instance.db","mode":"managed","type":"google_sql_database_instance","name":"db","change":{"actions":["create"],
			"after":{"region":"europe-west1","database_version":"POSTGRES_15","settings":[{"tier":"db-custom-1-3840","availability_type":"REGIONAL","disk_type":"PD_SSD"}]},
			"after_unknown":{"settings":[{"disk_size":true,"edition":true}]},"after_sensitive":{}}}],
		"configuration":{"root_module":{"resources":[
			{"address":"google_sql_database_instance.db","mode":"managed","type":"google_sql_database_instance","name":"db","provider_config_key":"google",
				"expressions":{"region":{"constant_value":"europe-west1"},"database_version":{"constant_value":"POSTGRES_15"},"settings":[{"tier":{"constant_value":"db-custom-1-3840"},"availability_type":{"constant_value":"REGIONAL"}}]}}]}}}`
	document, err := source.Read([]byte(plan))
	if err != nil {
		t.Fatal(err)
	}
	est := estimate(t, document.Set)

	if storage := component(est.GetResources()[0], "Storage"); storage.GetMonthlyQuantity() != "10" || pricing.Unpriced(storage) {
		t.Errorf("storage = %v, want the 10 GB an SSD disk starts at", storage)
	}
}

func TestADatabasePaysForPrivateServiceConnectOnlyWhenItIsEnabled(t *testing.T) {
	est := estimate(t, only("europe-west1", "database", "micro"))

	if endpoint := component(byID(est, "database"), "Private Service Connect endpoint"); endpoint.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_PRICED {
		t.Errorf("endpoint = %v, want it priced where psc_enabled is true", endpoint)
	}
	if strings.Contains(names(byID(est, "micro")), "Private Service Connect") || strings.Contains(names(byID(est, "micro")), "Data processed") {
		t.Errorf("micro = %s, want no endpoint where PSC is not enabled", names(byID(est, "micro")))
	}
}

func TestABackendWhoseCDNIsUnknownIsNeverFree(t *testing.T) {
	backend := resource("backend", "google_compute_backend_service", "europe-west1", map[string]any{})
	backend.Unknown = []string{"enable_cdn"}
	set := only("europe-west1")
	set.Resources = []*costv1.Resource{backend}
	est := byID(estimate(t, set), "backend")

	if est.GetStatus() == costv1.ResourceEstimate_STATUS_FREE || component(est, "Cache egress").GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_UNKNOWN_INPUT {
		t.Errorf("backend = %v, want its cache egress unpriced over enable_cdn", est)
	}
}

func TestUnsetSettingsPriceAtTheProvidersDocumentedDefaults(t *testing.T) {
	sql := func(version string) map[string]any {
		return map[string]any{"database_version": version, "settings": map[string]any{"tier": "db-custom-2-7680", "disk_size": 10}}
	}
	set := only("europe-west1", "database")
	set.Resources = append(set.Resources,
		resource("store", "google_memorystore_instance", "europe-west1", map[string]any{"node_type": "CUSTOM_PICO", "shard_count": 2}),
		resource("postgres15", "google_sql_database_instance", "europe-west1", sql("POSTGRES_15")),
		resource("postgres17", "google_sql_database_instance", "europe-west1", sql("POSTGRES_17")),
		resource("hyperdisk", "google_sql_database_instance", "europe-west1", map[string]any{
			"database_version": "POSTGRES_15", "settings": map[string]any{"tier": "db-custom-2-7680", "disk_type": "HYPERDISK_BALANCED"},
		}),
		resource("retaining", "google_pubsub_subscription", "europe-west1", map[string]any{"retain_acked_messages": true}),
		resource("forgetting", "google_pubsub_subscription", "europe-west1", map[string]any{}),
	)
	est := estimate(t, set)

	if node := component(byID(est, "store"), "Node"); node.GetMonthlyQuantity() != "1460" || pricing.Unpriced(node) {
		t.Errorf("store node = %v, want one node on each of 2 shards with no replicas", node)
	}
	zonalEnterprise := component(byID(est, "postgres15"), "vCPU")
	if pricing.Unpriced(zonalEnterprise) {
		t.Errorf("postgres 15 vCPU = %v, want it priced as a zonal Enterprise instance", zonalEnterprise)
	}
	if plus := component(byID(est, "postgres17"), "vCPU"); plus.GetSku() == zonalEnterprise.GetSku() {
		t.Errorf("postgres 17 vCPU = %v, want Enterprise Plus, the edition Cloud SQL gives POSTGRES_16 and later", plus)
	}
	if storage := component(byID(est, "hyperdisk"), "Storage"); storage.GetMonthlyQuantity() != "20" {
		t.Errorf("hyperdisk storage = %v, want the 20 GB a Hyperdisk Balanced disk starts at", storage)
	}
	if storage := component(byID(est, "retaining"), "Message storage"); storage.GetMonthlyQuantity() == "" || pricing.Unpriced(storage) {
		t.Errorf("retaining storage = %v, want acknowledged messages kept the default 7 days priced", storage)
	}
	if got := names(byID(est, "forgetting")); got != "Delivery throughput" {
		t.Errorf("forgetting = %s, want no message storage where retain_acked_messages is unset", got)
	}
}

func TestACloudRunServiceIdlesBetweenRequestsOnlyWhenItSetsNoResourcesOrSaysSo(t *testing.T) {
	stateShaped := func(resources []any) map[string]any {
		return map[string]any{
			"ingress": "INGRESS_TRAFFIC_ALL",
			"template": []any{map[string]any{
				"containers": []any{map[string]any{"resources": resources}},
				"scaling":    []any{map[string]any{"min_instance_count": 0}},
			}},
		}
	}
	limited := stateShaped([]any{map[string]any{"cpu_idle": nil, "limits": map[string]any{"cpu": "1", "memory": "512Mi"}}})
	blurred := resource("blurred", "google_cloud_run_v2_service", "us-central1", limited)
	blurred.Unknown = []string{"template.0.containers.0.resources.0.cpu_idle"}
	set := only("us-central1")
	set.Resources = []*costv1.Resource{
		resource("limited", "google_cloud_run_v2_service", "us-central1", limited),
		resource("bare", "google_cloud_run_v2_service", "us-central1", stateShaped([]any{})),
		blurred,
	}
	est := estimate(t, set)

	if cpu := component(byID(est, "limited"), "CPU, always allocated"); cpu.GetMonthlyQuantity() != "2628000" || cpu.GetMonthlyCost() != "47.30" {
		t.Errorf("limited cpu = %v, want a service that sets resources but not cpu_idle billed for every hour of its one vCPU", cpu)
	}
	if got := names(byID(est, "bare")); got != "Requests,CPU during requests,Memory during requests,Data transfer out to internet" {
		t.Errorf("bare = %s, want a service that sets no resources billed per request", got)
	}
	if cpu := component(byID(est, "bare"), "CPU during requests"); cpu.GetMonthlyQuantity() != "200000" || pricing.Unpriced(cpu) {
		t.Errorf("bare cpu = %v, want the default one vCPU for 200,000 seconds", cpu)
	}
	if memory := component(byID(est, "bare"), "Memory during requests"); memory.GetMonthlyQuantity() != "100000" || pricing.Unpriced(memory) {
		t.Errorf("bare memory = %v, want the default 512 MiB for 200,000 seconds", memory)
	}
	if got := names(byID(est, "blurred")); strings.Contains(got, "idle minimum instances") {
		t.Errorf("blurred = %s, want no idle minimum instances where min_instance_count is 0", got)
	}
	if cpu := component(byID(est, "blurred"), "CPU, always allocated"); cpu.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_UNKNOWN_INPUT {
		t.Errorf("blurred cpu = %v, want it unknown over cpu_idle", cpu)
	}
}

func TestAccessManagementIsFree(t *testing.T) {
	set := inventory("europe-west1")
	set.Resources = nil
	access := []string{"google_service_account", "google_project_iam_member", "google_project_iam_binding", "google_service_account_iam_member"}
	for _, typ := range access {
		set.Resources = append(set.Resources, resource(typ, typ, "europe-west1", map[string]any{}))
	}
	est := estimate(t, set)

	for _, typ := range access {
		if status := byID(est, typ).GetStatus(); status != costv1.ResourceEstimate_STATUS_FREE {
			t.Errorf("%s = %v, want it free", typ, status)
		}
	}
}
