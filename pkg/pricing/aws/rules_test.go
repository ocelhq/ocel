package aws_test

import (
	"context"
	"os"
	"slices"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/pricing/aws"
	"github.com/ocelhq/ocel/pkg/pricing/ingest"
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

func estimate(t *testing.T, card *pricingtest.Card, set *costv1.ResourceSet) *costv1.Estimate {
	t.Helper()
	est, err := pricing.Estimate(context.Background(), card, pricing.Combine(aws.Rules), set, nil)
	if err != nil {
		t.Fatal(err)
	}
	return est
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

var vendorUnits = map[string][]string{
	"Requests":                       {"Requests", "Request"},
	"Duration":                       {"Lambda-GB-Second"},
	"Ingested":                       {"GB"},
	"Stored":                         {"GB-Mo"},
	"Storage":                        {"GB-Mo"},
	"PUT, COPY, POST, LIST requests": {"Requests"},
	"GET and other requests":         {"Requests"},
	"I/O requests":                   {"IOs"},
	"Aurora Serverless v2 capacity":  {"ACU-Hr"},
	"Instance":                       {"Hrs"},
	"Secret":                         {"Secrets"},
	"API calls":                      {"API Requests"},
	"vCPU":                           {"hours"},
	"Memory":                         {"hours"},
	"Application load balancer":      {"Hrs"},
	"Load balancer capacity units":   {"LCU-Hrs"},
	"NAT gateway":                    {"Hrs"},
	"Data processed":                 {"GB"},
	"Public IPv4 address":            {"Hrs"},
	"Write request units":            {"WriteRequestUnits"},
	"Read request units":             {"ReadRequestUnits"},
	"Customer managed key":           {"Keys"},
	"Publish requests":               {"Requests"},
	"Invocations":                    {"Invocations", "FunctionExecutions"},
	"Reads":                          {"EdgeReads"},
	"Alarm metrics":                  {"Alarms"},
	"HTTPS requests":                 {"Requests"},
	"Data transfer out to internet":  {"GB"},
	"Event API operations":           {"Operation"},
	"Connection minutes":             {"Minute"},
	"Nodes":                          {"Hrs"},
	"Snapshot storage":               {"GB-Mo"},
}

func TestEveryComponentIsPricedByExactlyOneProductInTheUnitItCounts(t *testing.T) {
	card := catalog(t)
	raw, err := os.ReadFile("testdata/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	static, err := ingest.ReadStatic(raw)
	if err != nil {
		t.Fatal(err)
	}
	units := map[string]string{}
	for _, product := range static.Products {
		units[product.SKU] = product.Unit
	}
	for _, region := range fixtureRegions {
		t.Run(region, func(t *testing.T) {
			est := estimate(t, card, inventory(region))
			for _, r := range est.GetResources() {
				if r.GetStatus() == costv1.ResourceEstimate_STATUS_UNSUPPORTED {
					t.Errorf("%s is unsupported", r.GetResource())
				}
				for _, c := range r.GetComponents() {
					if c.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_PRICED {
						t.Errorf("%s %s = %v: %s", r.GetResource(), c.GetName(), c.GetPriceStatus(), c.GetPriceDetail())
						continue
					}
					if !slices.Contains(vendorUnits[c.GetName()], units[c.GetSku()]) {
						t.Errorf("%s %s is priced per %q, want one of %v", r.GetResource(), c.GetName(), units[c.GetSku()], vendorUnits[c.GetName()])
					}
				}
			}
		})
	}
}

func TestEveryProductTheRulesAskForIsOneTheManifestIngests(t *testing.T) {
	card := catalog(t)
	for _, region := range fixtureRegions {
		estimate(t, card, inventory(region))
	}

	if missed := card.MissedBy(aws.Rules.Ingest); len(missed) > 0 {
		t.Errorf("the manifest misses %v", missed)
	}
}

func TestAModerateLambdaMonthPricesRequestsAndDuration(t *testing.T) {
	lambda := byID(estimate(t, catalog(t), inventory("us-east-1")), "lambda")

	if requests := component(lambda, "Requests"); requests.GetMonthlyCost() != "0.20" {
		t.Errorf("requests = %v, want 0.20 (1M at 0.0000002)", requests)
	}
	if duration := component(lambda, "Duration"); duration.GetMonthlyCost() != "3.33" || duration.GetMonthlyQuantity() != "200000" {
		t.Errorf("duration = %v, want 3.33 (200,000 GB-seconds at 0.0000166667)", duration)
	}
}

func TestARegionalComponentIsPricedInItsResourcesRegion(t *testing.T) {
	card := catalog(t)
	virginia := component(byID(estimate(t, card, inventory("us-east-1")), "service"), "vCPU")
	london := component(byID(estimate(t, card, inventory("eu-west-2")), "service"), "vCPU")

	if virginia.GetUnitPrice() != "0.04048" || london.GetUnitPrice() != "0.04656" {
		t.Errorf("vCPU = %s in us-east-1 and %s in eu-west-2, want each region's own rate", virginia.GetUnitPrice(), london.GetUnitPrice())
	}
}

func TestLogsPriceTheCurrentVersionAndQueuesTheirOwnType(t *testing.T) {
	est := estimate(t, catalog(t), inventory("us-east-1"))

	if ingested := component(byID(est, "logs"), "Ingested"); ingested.GetSku() != "S8QGXX5R2BKKMDSJ" {
		t.Errorf("logs ingested = %v, want the Current version's SKU", ingested)
	}
	if standard, fifo := component(byID(est, "queue"), "Requests"), component(byID(est, "queue-fifo"), "Requests"); standard.GetUnitPrice() != "0.0000004" || fifo.GetUnitPrice() != "0.0000005" {
		t.Errorf("queues = %s standard and %s FIFO", standard.GetUnitPrice(), fifo.GetUnitPrice())
	}
}

func TestAnUnknownRegionLeavesCloudFrontPriced(t *testing.T) {
	set := inventory("")
	for _, r := range set.GetResources() {
		r.Unknown = append(r.Unknown, "region")
	}
	est := estimate(t, catalog(t), set)

	if requests := component(byID(est, "distribution"), "HTTPS requests"); requests.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_PRICED {
		t.Errorf("distribution requests = %v, want the global price", requests)
	}
	if requests := component(byID(est, "lambda"), "Requests"); requests.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_UNKNOWN_INPUT {
		t.Errorf("lambda requests = %v, want it unpriced over the unknown region", requests)
	}
}

func TestTopicsSpendTheFreeMillionPublishesOnce(t *testing.T) {
	set := inventory("us-east-1")
	set.Resources = []*costv1.Resource{
		resource("a", "aws_sns_topic", "us-east-1", map[string]any{}),
		resource("b", "aws_sns_topic", "us-east-1", map[string]any{}),
	}
	a, _ := structpb.NewStruct(map[string]any{"monthly_requests": 800_000})
	b, _ := structpb.NewStruct(map[string]any{"monthly_requests": 300_000})
	usage := &costv1.Usage{Resources: map[string]*structpb.Struct{"a": a, "b": b}}
	est, err := pricing.Estimate(context.Background(), catalog(t), pricing.Combine(aws.Rules), set, usage)
	if err != nil {
		t.Fatal(err)
	}

	if byID(est, "a").GetMonthlyUsage() != "0.00" || byID(est, "b").GetMonthlyUsage() != "0.05" {
		t.Errorf("topics = %s, %s; want the free 1M spent by 800,000 and 200,000, then 100,000 at 0.0000005", byID(est, "a").GetMonthlyUsage(), byID(est, "b").GetMonthlyUsage())
	}
	if !slices.Contains(est.GetNotes(), pricing.AllowanceAssumed) || !slices.Contains(est.GetNotes(), aws.Rules.Notes[0]) {
		t.Errorf("notes = %v, want the allowance assumption and the AWS free-tier note", est.GetNotes())
	}
}

func TestAnUnknownArchitectureOrCPUIsNeverGuessed(t *testing.T) {
	set := inventory("us-east-1")
	lambda := resource("lambda", "aws_lambda_function", "us-east-1", map[string]any{"memory_size": 1024})
	lambda.Unknown = []string{"architectures"}
	task := resource("task", "aws_ecs_task_definition", "us-east-1", map[string]any{"memory": "1024"})
	task.Unknown = []string{"cpu"}
	service := referencing(resource("service", "aws_ecs_service", "us-east-1", map[string]any{"desired_count": 1, "launch_type": "FARGATE"}), map[string]string{"task_definition": "task"})
	set.Resources = []*costv1.Resource{lambda, task, service}
	est := estimate(t, catalog(t), set)

	for _, c := range byID(est, "lambda").GetComponents() {
		if c.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_UNKNOWN_INPUT {
			t.Errorf("lambda %s = %v, want it unpriced rather than priced as x86", c.GetName(), c)
		}
	}
	if vcpu := component(byID(est, "service"), "vCPU"); vcpu.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_UNKNOWN_INPUT {
		t.Errorf("vCPU = %v, want it unpriced over the unknown cpu", vcpu)
	}
	if memory := component(byID(est, "service"), "Memory"); memory.GetMonthlyCost() != "3.24" {
		t.Errorf("memory = %v, want 3.24 (730 GB-hours at 0.004445)", memory)
	}
}

func TestACacheIsPricedPerNodeAndForSnapshotsPastTheFreeOne(t *testing.T) {
	cache := byID(estimate(t, catalog(t), inventory("us-east-1")), "cache")

	if nodes := component(cache, "Nodes"); nodes.GetMonthlyQuantity() != "1460" || nodes.GetMonthlyCost() != "18.69" {
		t.Errorf("nodes = %v, want 2 nodes for 730 hours at 0.0128", nodes)
	}
	if snapshots := component(cache, "Snapshot storage"); snapshots.GetMonthlyQuantity() != "20" {
		t.Errorf("snapshots = %v, want 10 GB for each of the 2 snapshots past the free one", snapshots)
	}
}

func TestACacheThatSetsNoEngineOrNodeCountRunsOneRedisNode(t *testing.T) {
	set := inventory("us-east-1")
	set.Resources = []*costv1.Resource{resource("bare", "aws_elasticache_replication_group", "us-east-1", map[string]any{"node_type": "cache.r7g.large"})}
	est := estimate(t, catalog(t), set)

	if nodes := component(byID(est, "bare"), "Nodes"); nodes.GetMonthlyQuantity() != "730" || pricing.Unpriced(nodes) {
		t.Errorf("nodes = %v, want one Redis node for 730 hours", nodes)
	}
}

func estimateDocument(t *testing.T, raw string) *costv1.Estimate {
	t.Helper()
	document, err := source.Read([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return estimate(t, catalog(t), document.Set)
}

func TestACacheGroupInClusterModeRunsEachShardsPrimaryAndReplicas(t *testing.T) {
	documents := map[string]string{
		"terraform plan": `{"format_version":"1.2","resource_changes":[
			{"address":"aws_elasticache_replication_group.cache","mode":"managed","type":"aws_elasticache_replication_group","name":"cache","change":{"actions":["create"],
				"after":{"region":"us-east-1","replication_group_id":"c","description":"c","node_type":"cache.r7g.large","engine":"redis","num_node_groups":3,"replicas_per_node_group":2},
				"after_unknown":{"num_cache_clusters":true,"member_clusters":true,"id":true},"after_sensitive":{}}}],
			"configuration":{"root_module":{"resources":[
				{"address":"aws_elasticache_replication_group.cache","mode":"managed","type":"aws_elasticache_replication_group","name":"cache","provider_config_key":"aws",
					"expressions":{"replication_group_id":{"constant_value":"c"},"description":{"constant_value":"c"},"node_type":{"constant_value":"cache.r7g.large"},
						"engine":{"constant_value":"redis"},"num_node_groups":{"constant_value":3},"replicas_per_node_group":{"constant_value":2}}}]}}}`,
		"pulumi preview": `{"config":{"aws:region":"us-east-1"},"steps":[
			{"op":"create","urn":"urn:pulumi:dev::p::aws:elasticache/replicationGroup:ReplicationGroup::cache","newState":{
				"urn":"urn:pulumi:dev::p::aws:elasticache/replicationGroup:ReplicationGroup::cache","custom":true,"type":"aws:elasticache/replicationGroup:ReplicationGroup",
				"inputs":{"description":"c","engine":"redis","nodeType":"cache.r7g.large","numNodeGroups":3,"replicasPerNodeGroup":2,"region":"us-east-1"},
				"outputs":{"description":"c","engine":"redis","nodeType":"cache.r7g.large","numNodeGroups":3,"replicasPerNodeGroup":2,"region":"us-east-1",
					"numCacheClusters":"04da6b54-80e4-46f7-96ec-b56ff0331ba9","memberClusters":"04da6b54-80e4-46f7-96ec-b56ff0331ba9","id":"04da6b54-80e4-46f7-96ec-b56ff0331ba9"}}}]}`,
	}
	for name, raw := range documents {
		t.Run(name, func(t *testing.T) {
			est := estimateDocument(t, raw)

			if nodes := component(est.GetResources()[0], "Nodes"); nodes.GetMonthlyQuantity() != "6570" || pricing.Unpriced(nodes) {
				t.Errorf("nodes = %v, want 3 shards of a primary and 2 replicas for 730 hours", nodes)
			}
		})
	}
}

func TestAnSSTRedisRunsOneNodePerShard(t *testing.T) {
	est := estimateDocument(t, `[
		{"op":"create","urn":"urn:pulumi:production::shop::sst:aws:Redis$aws:elasticache/replicationGroup:ReplicationGroup::CacheCluster","type":"aws:elasticache/replicationGroup:ReplicationGroup",
			"new":{"type":"aws:elasticache/replicationGroup:ReplicationGroup","urn":"urn:pulumi:production::shop::sst:aws:Redis$aws:elasticache/replicationGroup:ReplicationGroup::CacheCluster","custom":true,"id":"",
				"inputs":{"automaticFailoverEnabled":true,"clusterMode":"enabled","description":"Managed by SST","engine":"valkey","nodeType":"cache.t4g.micro",
					"numNodeGroups":2,"replicasPerNodeGroup":0,"region":"us-east-1"},
				"outputs":{"clusterMode":"enabled","engine":"valkey","nodeType":"cache.t4g.micro","numNodeGroups":2,"replicasPerNodeGroup":0,"region":"us-east-1",
					"numCacheClusters":"04da6b54-80e4-46f7-96ec-b56ff0331ba9","id":"04da6b54-80e4-46f7-96ec-b56ff0331ba9"}},
			"detailedDiff":null,"logical":true,"provider":""}]`)

	if nodes := component(est.GetResources()[0], "Nodes"); nodes.GetMonthlyQuantity() != "1460" || pricing.Unpriced(nodes) {
		t.Errorf("nodes = %v, want 2 shards of one primary each for 730 hours", nodes)
	}
}

func TestACacheGroupWhoseNodeTypeTheProviderChoosesIsUnpricedSayingSo(t *testing.T) {
	set := inventory("us-east-1")
	set.Resources = []*costv1.Resource{resource("chosen", "aws_elasticache_replication_group", "us-east-1", map[string]any{"engine": "redis"})}
	est := estimate(t, catalog(t), set)

	if nodes := component(byID(est, "chosen"), "Nodes"); nodes.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_MISSING ||
		nodes.GetPriceDetail() != "node_type is not set, so the provider chooses it on apply" {
		t.Errorf("nodes = %v, want it unpriced because the provider chooses node_type on apply", nodes)
	}
}

func TestAServerlessInstanceScalesWithinItsClustersCapacity(t *testing.T) {
	set := inventory("us-east-1")
	set.Resources = []*costv1.Resource{
		resource("cluster", "aws_rds_cluster", "us-east-1", map[string]any{
			"engine": "aurora-postgresql", "serverlessv2_scaling_configuration": []any{map[string]any{"min_capacity": 1, "max_capacity": 3}},
		}),
		referencing(resource("writer", "aws_rds_cluster_instance", "us-east-1", map[string]any{"engine": "aurora-postgresql", "instance_class": "db.serverless"}),
			map[string]string{"cluster_identifier": "cluster"}),
		resource("orphan", "aws_rds_cluster_instance", "us-east-1", map[string]any{"engine": "aurora-postgresql", "instance_class": "db.serverless"}),
	}
	est := estimate(t, catalog(t), set)

	if capacity := component(byID(est, "writer"), "Aurora Serverless v2 capacity"); capacity.GetMonthlyQuantity() != "1460" {
		t.Errorf("capacity = %v, want 2 ACUs, the middle of its cluster's 1 to 3, for 730 hours", capacity)
	}
	if capacity := component(byID(est, "orphan"), "Aurora Serverless v2 capacity"); capacity.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_MISSING ||
		capacity.GetPriceDetail() != "cluster_identifier is not set" {
		t.Errorf("capacity without a cluster = %v, want it unpriced because cluster_identifier is not set", capacity)
	}
}

func TestAFargateServiceRunsItsTaskDefinitionsShape(t *testing.T) {
	est := estimate(t, catalog(t), inventory("us-east-1"))

	if vcpu := component(byID(est, "service"), "vCPU"); vcpu.GetMonthlyQuantity() != "730" {
		t.Errorf("vcpu = %v, want 2 tasks of half a vCPU for 730 hours", vcpu)
	}
	if memory := component(byID(est, "service-arm"), "Memory"); memory.GetMonthlyQuantity() != "365" || memory.GetSku() == component(byID(est, "service"), "Memory").GetSku() {
		t.Errorf("arm memory = %v, want one task of 0.5 GB for 730 hours at the ARM rate", memory)
	}
}

func fargateService(id string, props map[string]any) *costv1.Resource {
	return referencing(resource(id, "aws_ecs_service", "us-east-1", props), map[string]string{"task_definition": "task"})
}

func TestAServiceBillsFargateTimeOnlyForTheTasksItRunsOnFargate(t *testing.T) {
	set := inventory("us-east-1")
	unknownLaunch := fargateService("unknown", map[string]any{"desired_count": 1})
	unknownLaunch.Unknown = []string{"launch_type"}
	set.Resources = []*costv1.Resource{
		resource("task", "aws_ecs_task_definition", "us-east-1", map[string]any{"cpu": "512", "memory": "1024"}),
		fargateService("ec2", map[string]any{"desired_count": 4, "launch_type": "EC2"}),
		fargateService("ec2-provider", map[string]any{"desired_count": 4, "capacity_provider_strategy": []any{map[string]any{"capacity_provider": "asg", "weight": 1}}}),
		fargateService("mixed", map[string]any{"desired_count": 5, "capacity_provider_strategy": []any{
			map[string]any{"capacity_provider": "FARGATE", "weight": 1, "base": 1},
			map[string]any{"capacity_provider": "FARGATE_SPOT", "weight": 3},
		}}),
		unknownLaunch,
		fargateService("unset", map[string]any{"desired_count": 1}),
		fargateService("unset-uncounted", map[string]any{}),
		fargateService("uncounted", map[string]any{"launch_type": "FARGATE"}),
		fargateService("fargate", map[string]any{"desired_count": 1, "launch_type": "FARGATE"}),
	}
	est := estimate(t, catalog(t), set)

	for _, id := range []string{"ec2", "ec2-provider"} {
		if r := byID(est, id); r.GetStatus() != costv1.ResourceEstimate_STATUS_FREE {
			t.Errorf("%s = %v, want it free with its instances billed where they run", id, r)
		}
	}
	mixed := byID(est, "mixed")
	if vcpu := component(mixed, "vCPU"); vcpu.GetMonthlyQuantity() != "730" {
		t.Errorf("vCPU = %v, want the base task and 1 of the 4 weighted ones, at half a vCPU for 730 hours", vcpu)
	}
	if spot := component(mixed, "Spot vCPU"); spot.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_MISSING || spot.GetMonthlyQuantity() != "1095" {
		t.Errorf("spot vCPU = %v, want the 3 Spot tasks shown without a price", spot)
	}
	unknown := byID(est, "unknown")
	if len(unknown.GetComponents()) == 0 {
		t.Errorf("unknown has no components, want them shown unpriced")
	}
	for _, c := range unknown.GetComponents() {
		if c.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_UNKNOWN_INPUT || !slices.Contains(c.GetDependsOnUnknown(), "launch_type") {
			t.Errorf("unknown %s = %v, want it unknown over launch_type", c.GetName(), c)
		}
	}
	unset := byID(est, "unset")
	if len(unset.GetComponents()) == 0 {
		t.Errorf("unset = %v, want its Fargate components shown unpriced rather than free", unset)
	}
	for _, c := range unset.GetComponents() {
		if c.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_MISSING ||
			c.GetPriceDetail() != "launch_type is not set, so the cluster's default capacity provider strategy decides" {
			t.Errorf("unset %s = %v, want it unpriced because the cluster's default strategy decides", c.GetName(), c)
		}
	}
	for _, id := range []string{"unset-uncounted", "uncounted"} {
		if r := byID(est, id); r.GetStatus() != costv1.ResourceEstimate_STATUS_FREE {
			t.Errorf("%s = %v, want it free: desired_count defaults to 0", id, r)
		}
	}
	if vcpu := component(byID(est, "fargate"), "vCPU"); vcpu.GetMonthlyQuantity() != "365" || pricing.Unpriced(vcpu) {
		t.Errorf("fargate vCPU = %v, want one task of half a vCPU for 730 hours, priced", vcpu)
	}
}

func TestAFargateTaskWrittenInVCPUAndGBRunsThatShape(t *testing.T) {
	set := inventory("us-east-1")
	set.Resources = []*costv1.Resource{
		resource("task", "aws_ecs_task_definition", "us-east-1", map[string]any{"cpu": "1 vCPU", "memory": "2 GB"}),
		resource("small", "aws_ecs_task_definition", "us-east-1", map[string]any{"cpu": ".25 vcpu", "memory": "512"}),
		fargateService("service", map[string]any{"desired_count": 1, "launch_type": "FARGATE"}),
		referencing(resource("small-service", "aws_ecs_service", "us-east-1", map[string]any{"desired_count": 1, "launch_type": "FARGATE"}), map[string]string{"task_definition": "small"}),
	}
	est := estimate(t, catalog(t), set)

	for id, want := range map[string][2]string{"service": {"730", "1460"}, "small-service": {"182.5", "365"}} {
		vcpu, memory := component(byID(est, id), "vCPU"), component(byID(est, id), "Memory")
		if vcpu.GetMonthlyQuantity() != want[0] || memory.GetMonthlyQuantity() != want[1] || pricing.Unpriced(vcpu) || pricing.Unpriced(memory) {
			t.Errorf("%s = %v and %v, want %v vCPU-hours and GB-hours", id, vcpu, memory, want)
		}
	}
}

func TestAnAlarmBillsEachMetricItWatches(t *testing.T) {
	set := inventory("us-east-1")
	set.Resources = []*costv1.Resource{
		resource("named", "aws_cloudwatch_metric_alarm", "us-east-1", map[string]any{"metric_name": "Errors"}),
		resource("math", "aws_cloudwatch_metric_alarm", "us-east-1", map[string]any{"metric_query": []any{
			map[string]any{"id": "e1", "expression": "m1 / m2"},
			map[string]any{"id": "m1", "metric": []any{map[string]any{"metric_name": "Errors"}}},
			map[string]any{"id": "m2", "metric": []any{map[string]any{"metric_name": "Invocations"}}},
		}}),
		resource("bare", "aws_cloudwatch_metric_alarm", "us-east-1", map[string]any{}),
	}
	est := estimate(t, catalog(t), set)

	for id, want := range map[string]string{"named": "1", "math": "2"} {
		if metrics := component(byID(est, id), "Alarm metrics"); metrics.GetMonthlyQuantity() != want {
			t.Errorf("%s = %v, want %s alarm metrics", id, metrics, want)
		}
	}
	if metrics := component(byID(est, "bare"), "Alarm metrics"); metrics.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_MISSING {
		t.Errorf("an alarm naming no metric = %v, want it unpriced", metrics)
	}
}

func TestAKeyValueStoreBillsReadsPastTheFreeTwoMillionAndEachAPICall(t *testing.T) {
	set := inventory("us-east-1")
	set.Resources = []*costv1.Resource{resource("store", "aws_cloudfront_key_value_store", "us-east-1", map[string]any{})}
	overrides, _ := structpb.NewStruct(map[string]any{"monthly_reads": 12_000_000, "monthly_api_calls": 500})
	for name, tc := range map[string]struct {
		usage      *costv1.Usage
		reads, api string
	}{
		"light":    {usage: &costv1.Usage{Profile: costv1.Profile_PROFILE_LIGHT}, reads: "0.00", api: "0.10"},
		"moderate": {usage: &costv1.Usage{Profile: costv1.Profile_PROFILE_MODERATE}, reads: "0.00", api: "1.00"},
		"heavy":    {usage: &costv1.Usage{Profile: costv1.Profile_PROFILE_HEAVY}, reads: "0.24", api: "10.00"},
		"usage file": {usage: &costv1.Usage{Profile: costv1.Profile_PROFILE_HEAVY, Resources: map[string]*structpb.Struct{"store": overrides}},
			reads: "0.30", api: "0.50"},
	} {
		t.Run(name, func(t *testing.T) {
			est, err := pricing.Estimate(context.Background(), catalog(t), pricing.Combine(aws.Rules), set, tc.usage)
			if err != nil {
				t.Fatal(err)
			}
			store := byID(est, "store")
			if store.GetStatus() != costv1.ResourceEstimate_STATUS_PRICED {
				t.Fatalf("store = %v, want it priced", store.GetStatus())
			}
			if reads, api := component(store, "Reads"), component(store, "API calls"); reads.GetMonthlyCost() != tc.reads || api.GetMonthlyCost() != tc.api {
				t.Errorf("reads = %v, API calls = %v; want %s and %s", reads, api, tc.reads, tc.api)
			}
		})
	}
}

func TestADiffAddingAccessManagementAddsNothing(t *testing.T) {
	current := inventory("us-east-1")
	current.Resources = []*costv1.Resource{resource("lambda", "aws_lambda_function", "us-east-1", map[string]any{"memory_size": 1024})}
	proposed := inventory("us-east-1")
	proposed.Resources = append([]*costv1.Resource{}, current.GetResources()...)
	access := []string{"aws_iam_role", "aws_iam_policy", "aws_iam_role_policy", "aws_iam_role_policy_attachment", "aws_iam_instance_profile"}
	for _, typ := range access {
		proposed.Resources = append(proposed.Resources, resource(typ, typ, "us-east-1", map[string]any{}))
	}
	card := catalog(t)

	diff := pricing.Diff(current, proposed, estimate(t, card, current), estimate(t, card, proposed))

	if diff.GetMonthlyFixedDelta() != "0.00" || diff.GetMonthlyUsageDelta() != "0.00" {
		t.Errorf("totals = %s + %s, want nothing added", diff.GetMonthlyFixedDelta(), diff.GetMonthlyUsageDelta())
	}
	if len(diff.GetResources()) != len(access) {
		t.Fatalf("changes = %v, want one per access-management resource", diff.GetResources())
	}
	for _, change := range diff.GetResources() {
		if change.GetChange() != costv1.ResourceChange_CHANGE_ADDED || change.GetMonthlyFixedDelta() != "0.00" || change.GetMonthlyUsageDelta() != "0.00" {
			t.Errorf("%s = %v, want it added at 0.00 + 0.00", change.GetResource(), change)
		}
	}
}
