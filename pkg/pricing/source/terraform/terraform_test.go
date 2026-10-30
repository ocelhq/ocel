package terraform_test

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/pricing/source/internal/golden"
	"github.com/ocelhq/ocel/pkg/pricing/source/terraform"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestEachFixtureReadsToItsGoldenResourceSets(t *testing.T) {
	for _, name := range []string{"plan-create", "plan-update"} {
		t.Run(name, func(t *testing.T) {
			set, prior, err := terraform.ReadPlan(fixture(t, name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			golden.Match(t, name, set, prior)
		})
	}
	t.Run("state", func(t *testing.T) {
		set, err := terraform.ReadState(fixture(t, "state.json"))
		if err != nil {
			t.Fatal(err)
		}
		golden.Match(t, "state", set, nil)
	})
}

func plan(t *testing.T, doc string) (*costv1.ResourceSet, *costv1.ResourceSet) {
	t.Helper()
	set, prior, err := terraform.ReadPlan([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return set, prior
}

func byAddress(set *costv1.ResourceSet) map[string]*costv1.Resource {
	out := map[string]*costv1.Resource{}
	for _, resource := range set.GetResources() {
		out[resource.GetId()] = resource
	}
	return out
}

func TestAListHoldingOneObjectFlattensToThatObject(t *testing.T) {
	set, _ := plan(t, `{"format_version":"1.2","resource_changes":[{"address":"aws_rds_cluster.main","mode":"managed","type":"aws_rds_cluster","name":"main",
		"change":{"actions":["create"],"after":{"region":"eu-west-1","serverlessv2_scaling_configuration":[{"min_capacity":0.5,"max_capacity":4}],
			"availability_zones":["eu-west-1a","eu-west-1b"],"tags":{"team":"db"}},
		"after_unknown":{"serverlessv2_scaling_configuration":[{"seconds_until_auto_pause":true}]},"after_sensitive":{}}}]}`)
	resource := byAddress(set)["aws_rds_cluster.main"]
	fields := resource.GetProperties().GetFields()
	scaling := fields["serverlessv2_scaling_configuration"].GetStructValue().GetFields()
	if scaling["min_capacity"].GetNumberValue() != 0.5 {
		t.Errorf("scaling = %v", fields["serverlessv2_scaling_configuration"])
	}
	if len(fields["availability_zones"].GetListValue().GetValues()) != 2 {
		t.Errorf("availability_zones = %v", fields["availability_zones"])
	}
	if want := []string{"serverlessv2_scaling_configuration.seconds_until_auto_pause"}; !slices.Equal(resource.GetUnknown(), want) {
		t.Errorf("unknown = %v, want %v", resource.GetUnknown(), want)
	}
	if resource.GetTags()["team"] != "db" {
		t.Errorf("tags = %v", resource.GetTags())
	}
}

func TestASensitiveValueIsDroppedAndListedUnknown(t *testing.T) {
	set, _ := plan(t, `{"format_version":"1.2","resource_changes":[{"address":"aws_lambda_function.fn","mode":"managed","type":"aws_lambda_function","name":"fn",
		"change":{"actions":["create"],"after":{"region":"us-east-1","environment":[{"variables":{"API_KEY":"hunter2","STAGE":"dev"}}],"memory_size":512},
		"after_unknown":{},"after_sensitive":{"environment":[{"variables":{"API_KEY":true}}]}}}]}`)
	resource := byAddress(set)["aws_lambda_function.fn"]
	variables := resource.GetProperties().GetFields()["environment"].GetStructValue().GetFields()["variables"].GetStructValue().GetFields()
	if _, carried := variables["API_KEY"]; carried {
		t.Error("the sensitive value was carried")
	}
	if variables["STAGE"].GetStringValue() != "dev" {
		t.Errorf("variables = %v", variables)
	}
	if want := []string{"environment.variables.API_KEY"}; !slices.Equal(resource.GetUnknown(), want) {
		t.Errorf("unknown = %v, want %v", resource.GetUnknown(), want)
	}
}

func TestAnUnknownElementMarksTheWholeListUnknown(t *testing.T) {
	set, _ := plan(t, `{"format_version":"1.2","resource_changes":[{"address":"aws_instance.vm","mode":"managed","type":"aws_instance","name":"vm",
		"change":{"actions":["create"],"after":{"region":"us-east-1","vpc_security_group_ids":["sg-1",null]},
		"after_unknown":{"vpc_security_group_ids":[false,true]},"after_sensitive":{}}}]}`)
	resource := byAddress(set)["aws_instance.vm"]
	if _, carried := resource.GetProperties().GetFields()["vpc_security_group_ids"]; carried {
		t.Error("the partly unknown list was carried")
	}
	if want := []string{"vpc_security_group_ids"}; !slices.Equal(resource.GetUnknown(), want) {
		t.Errorf("unknown = %v, want %v", resource.GetUnknown(), want)
	}
}

func TestPlanSetsTakeEveryActionButADeleteAndThePriorTakesThePriorState(t *testing.T) {
	set, prior := plan(t, `{"format_version":"1.2",
		"prior_state":{"format_version":"1.0","values":{"root_module":{"resources":[
			{"address":"aws_sqs_queue.gone","mode":"managed","type":"aws_sqs_queue","name":"gone","values":{"region":"us-east-1"},"sensitive_values":{}},
			{"address":"data.aws_region.current","mode":"data","type":"aws_region","name":"current","values":{"name":"us-east-1"},"sensitive_values":{}}]}}},
		"resource_changes":[
			{"address":"aws_sqs_queue.gone","mode":"managed","type":"aws_sqs_queue","name":"gone","change":{"actions":["delete"],"before":{"region":"us-east-1"},"after":null}},
			{"address":"aws_sqs_queue.kept","mode":"managed","type":"aws_sqs_queue","name":"kept","change":{"actions":["no-op"],"after":{"region":"us-east-1"},"after_unknown":{},"after_sensitive":{}}},
			{"address":"aws_sqs_queue.swapped","mode":"managed","type":"aws_sqs_queue","name":"swapped","change":{"actions":["delete","create"],"after":{"region":"us-east-1"},"after_unknown":{},"after_sensitive":{}}},
			{"address":"aws_sqs_queue.swapped","mode":"managed","type":"aws_sqs_queue","name":"swapped","deposed":"00000001","change":{"actions":["delete"],"before":{"region":"us-east-1"},"after":null}},
			{"address":"data.aws_region.current","mode":"data","type":"aws_region","name":"current","change":{"actions":["read"],"after":{"name":"us-east-1"}}}]}`)
	if got := ids(set); !slices.Equal(got, []string{"aws_sqs_queue.kept", "aws_sqs_queue.swapped"}) {
		t.Errorf("set = %v", got)
	}
	if got := ids(prior); !slices.Equal(got, []string{"aws_sqs_queue.gone"}) {
		t.Errorf("prior = %v", got)
	}
}

func ids(set *costv1.ResourceSet) []string {
	var out []string
	for _, resource := range set.GetResources() {
		out = append(out, resource.GetId())
	}
	return out
}

func TestRegionResolvesFromTheResourceThenItsProviderConfiguration(t *testing.T) {
	set, _ := plan(t, `{"format_version":"1.2",
		"variables":{"region":{"value":"us-east-1"},"zone":{"value":"asia-east1-a"},"count":{"value":2}},
		"configuration":{
			"provider_config":{
				"aws":{"name":"aws","expressions":{"region":{"constant_value":"eu-west-2"}}},
				"aws.east":{"name":"aws","alias":"east","expressions":{"region":{"references":["var.region"]}}},
				"aws.computed":{"name":"aws","alias":"computed","expressions":{"region":{"references":["data.aws_region.current.name","data.aws_region.current"]}}},
				"aws.numbered":{"name":"aws","alias":"numbered","expressions":{"region":{"references":["var.count"]}}},
				"aws.unset":{"name":"aws","alias":"unset","expressions":{"region":{"references":["var.missing"]}}},
				"module.db:aws":{"name":"aws","module_address":"module.db","expressions":{"region":{"references":["var.region"]}}},
				"google":{"name":"google","expressions":{"zone":{"constant_value":"europe-west1-b"}}},
				"google.taipei":{"name":"google","alias":"taipei","expressions":{"zone":{"references":["var.zone"]}}}},
			"root_module":{
				"resources":[
					{"address":"aws_sqs_queue.own","provider_config_key":"aws"},
					{"address":"aws_sqs_queue.inherited","provider_config_key":"aws"},
					{"address":"aws_sqs_queue.referenced","provider_config_key":"aws.east"},
					{"address":"aws_sqs_queue.computed_provider","provider_config_key":"aws.computed"},
					{"address":"aws_sqs_queue.numbered","provider_config_key":"aws.numbered"},
					{"address":"aws_sqs_queue.unset","provider_config_key":"aws.unset"},
					{"address":"google_pubsub_topic.zoned","provider_config_key":"google"},
					{"address":"google_pubsub_topic.taipei","provider_config_key":"google.taipei"}],
				"module_calls":{
					"db":{"module":{"resources":[{"address":"aws_sqs_queue.nested","provider_config_key":"module.db:aws"}]}},
					"cache":{"module":{"resources":[{"address":"aws_sqs_queue.nested","provider_config_key":"aws"}]}}}}},
		"resource_changes":[
			{"address":"aws_sqs_queue.own","mode":"managed","type":"aws_sqs_queue","name":"own","change":{"actions":["create"],"after":{"region":"ap-south-1"},"after_unknown":{}}},
			{"address":"aws_sqs_queue.inherited","mode":"managed","type":"aws_sqs_queue","name":"inherited","change":{"actions":["create"],"after":{},"after_unknown":{}}},
			{"address":"aws_sqs_queue.referenced","mode":"managed","type":"aws_sqs_queue","name":"referenced","change":{"actions":["create"],"after":{},"after_unknown":{}}},
			{"address":"aws_sqs_queue.computed_provider","mode":"managed","type":"aws_sqs_queue","name":"computed_provider","change":{"actions":["create"],"after":{},"after_unknown":{}}},
			{"address":"aws_sqs_queue.numbered","mode":"managed","type":"aws_sqs_queue","name":"numbered","change":{"actions":["create"],"after":{},"after_unknown":{}}},
			{"address":"aws_sqs_queue.unset","mode":"managed","type":"aws_sqs_queue","name":"unset","change":{"actions":["create"],"after":{},"after_unknown":{}}},
			{"address":"google_pubsub_topic.zoned","mode":"managed","type":"google_pubsub_topic","name":"zoned","change":{"actions":["create"],"after":{},"after_unknown":{}}},
			{"address":"google_pubsub_topic.taipei","mode":"managed","type":"google_pubsub_topic","name":"taipei","change":{"actions":["create"],"after":{},"after_unknown":{}}},
			{"address":"module.db.aws_sqs_queue.nested[0]","module_address":"module.db","mode":"managed","type":"aws_sqs_queue","name":"nested","index":0,"change":{"actions":["create"],"after":{},"after_unknown":{}}},
			{"address":"module.cache.aws_sqs_queue.nested[0]","module_address":"module.cache","mode":"managed","type":"aws_sqs_queue","name":"nested","index":0,"change":{"actions":["create"],"after":{},"after_unknown":{}}},
			{"address":"aws_sqs_queue.computed","mode":"managed","type":"aws_sqs_queue","name":"computed","change":{"actions":["create"],"after":{},"after_unknown":{"region":true}}}]}`)
	want := map[string]string{
		"aws_sqs_queue.own":                    "ap-south-1",
		"aws_sqs_queue.inherited":              "eu-west-2",
		"aws_sqs_queue.referenced":             "us-east-1",
		"aws_sqs_queue.computed_provider":      "",
		"aws_sqs_queue.numbered":               "",
		"aws_sqs_queue.unset":                  "",
		"google_pubsub_topic.zoned":            "europe-west1",
		"google_pubsub_topic.taipei":           "asia-east1",
		"module.db.aws_sqs_queue.nested[0]":    "",
		"module.cache.aws_sqs_queue.nested[0]": "eu-west-2",
		"aws_sqs_queue.computed":               "",
	}
	for id, resource := range byAddress(set) {
		if resource.GetRegion() != want[id] {
			t.Errorf("%s region = %q, want %q", id, resource.GetRegion(), want[id])
		}
		if slices.Contains(resource.GetUnknown(), "region") != (want[id] == "") {
			t.Errorf("%s unknown = %v", id, resource.GetUnknown())
		}
	}
	if len(set.GetResources()) != len(want) {
		t.Errorf("got %d resources, want %d", len(set.GetResources()), len(want))
	}
}

func TestAGCPResourceInAStateTakesItsRegionFromItsLocation(t *testing.T) {
	set, err := terraform.ReadState([]byte(`{"format_version":"1.0","values":{"root_module":{"resources":[
		{"address":"google_cloud_run_v2_service.web","mode":"managed","type":"google_cloud_run_v2_service","name":"web","values":{"location":"europe-west1"}},
		{"address":"google_storage_bucket.multi","mode":"managed","type":"google_storage_bucket","name":"multi","values":{"location":"US"}},
		{"address":"google_compute_instance.vm","mode":"managed","type":"google_compute_instance","name":"vm","values":{"zone":"us-central1-a","location":null}}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"google_cloud_run_v2_service.web": "europe-west1", "google_storage_bucket.multi": "", "google_compute_instance.vm": ""}
	for id, resource := range byAddress(set) {
		if resource.GetRegion() != want[id] {
			t.Errorf("%s region = %q, want %q", id, resource.GetRegion(), want[id])
		}
		if slices.Contains(resource.GetUnknown(), "region") != (want[id] == "") {
			t.Errorf("%s unknown = %v", id, resource.GetUnknown())
		}
	}
}

func TestAGCPMultiRegionLocationOutranksTheProviderConfiguration(t *testing.T) {
	set, _ := plan(t, `{"format_version":"1.2",
		"configuration":{"provider_config":{"google":{"name":"google","expressions":{"region":{"constant_value":"europe-west1"}}}},
			"root_module":{"resources":[{"address":"google_storage_bucket.multi","provider_config_key":"google"},{"address":"google_pubsub_topic.t","provider_config_key":"google"}]}},
		"resource_changes":[
			{"address":"google_storage_bucket.multi","mode":"managed","type":"google_storage_bucket","name":"multi","change":{"actions":["create"],"after":{"location":"EU"},"after_unknown":{}}},
			{"address":"google_pubsub_topic.t","mode":"managed","type":"google_pubsub_topic","name":"t","change":{"actions":["create"],"after":{},"after_unknown":{}}}]}`)
	resources := byAddress(set)
	if region := resources["google_storage_bucket.multi"].GetRegion(); region != "" {
		t.Errorf("multi-region bucket region = %q, want unknown", region)
	}
	if region := resources["google_pubsub_topic.t"].GetRegion(); region != "europe-west1" {
		t.Errorf("topic region = %q, want the provider's", region)
	}
}

func TestModulesNestAsScopesUnderTheRoot(t *testing.T) {
	set, err := terraform.ReadState([]byte(`{"format_version":"1.0","values":{"root_module":{
		"resources":[{"address":"aws_sqs_queue.top","mode":"managed","type":"aws_sqs_queue","name":"top","values":{"region":"us-east-1"}}],
		"child_modules":[{"address":"module.app[\"a.b\"]","resources":[],"child_modules":[{"address":"module.app[\"a.b\"].module.db",
			"resources":[{"address":"module.app[\"a.b\"].module.db.aws_rds_cluster.main","mode":"managed","type":"aws_rds_cluster","name":"main","values":{"region":"us-east-1"}}]}]}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	var got [][3]string
	for _, scope := range set.GetScopes() {
		got = append(got, [3]string{scope.GetId(), scope.GetParent(), scope.GetName()})
	}
	want := [][3]string{
		{"root", "", "root"},
		{`module.app["a.b"]`, "root", `module.app["a.b"]`},
		{`module.app["a.b"].module.db`, `module.app["a.b"]`, `module.app["a.b"].module.db`},
	}
	if !slices.Equal(got, want) {
		t.Errorf("scopes = %v, want %v", got, want)
	}
	resources := byAddress(set)
	if resources[`module.app["a.b"].module.db.aws_rds_cluster.main`].GetScope() != `module.app["a.b"].module.db` || resources["aws_sqs_queue.top"].GetScope() != "root" {
		t.Errorf("resources = %v", set.GetResources())
	}
	if resources[`module.app["a.b"].module.db.aws_rds_cluster.main`].GetName() != "main" {
		t.Errorf("name = %q", resources[`module.app["a.b"].module.db.aws_rds_cluster.main`].GetName())
	}
}

func TestAPlanReferencesTheResourceAConfigurationExpressionNames(t *testing.T) {
	set, _ := plan(t, `{"format_version":"1.2",
		"configuration":{"root_module":{
			"resources":[
				{"address":"aws_ecs_task_definition.app","mode":"managed","type":"aws_ecs_task_definition","name":"app","expressions":{"family":{"constant_value":"app"}}},
				{"address":"aws_ecs_service.app","mode":"managed","type":"aws_ecs_service","name":"app","expressions":{
					"task_definition":{"references":["aws_ecs_task_definition.app.arn","aws_ecs_task_definition.app"]},
					"cluster":{"references":["data.aws_ecs_cluster.shared.arn","data.aws_ecs_cluster.shared"]},
					"desired_count":{"references":["var.count"]},
					"network_configuration":[{"subnets":{"references":["aws_subnet.a.id","aws_subnet.a","aws_subnet.b.id","aws_subnet.b"]}}],
					"load_balancer":[{"target_group_arn":{"references":["aws_lb_target_group.web[1].arn","aws_lb_target_group.web[1]","aws_lb_target_group.web"]}}],
					"deployment_circuit_breaker":{"rollback":{"references":["aws_sqs_queue.gone.arn","aws_sqs_queue.gone"]}},
					"tags":{"references":["aws_sqs_queue.many.url","aws_sqs_queue.many"]}}},
				{"address":"aws_subnet.a","mode":"managed","type":"aws_subnet","name":"a","expressions":{}},
				{"address":"aws_subnet.b","mode":"managed","type":"aws_subnet","name":"b","expressions":{}},
				{"address":"aws_lb_target_group.web","mode":"managed","type":"aws_lb_target_group","name":"web","expressions":{}},
				{"address":"aws_sqs_queue.many","mode":"managed","type":"aws_sqs_queue","name":"many","expressions":{}}],
			"module_calls":{"db":{"module":{"resources":[
				{"address":"aws_rds_cluster.main","mode":"managed","type":"aws_rds_cluster","name":"main","expressions":{}},
				{"address":"aws_rds_cluster_instance.main","mode":"managed","type":"aws_rds_cluster_instance","name":"main","expressions":{
					"cluster_identifier":{"references":["aws_rds_cluster.main.id","aws_rds_cluster.main"]}}}]}}}}},
		"resource_changes":[
			{"address":"aws_ecs_task_definition.app","mode":"managed","type":"aws_ecs_task_definition","name":"app","change":{"actions":["create"],"after":{"region":"us-east-1"},"after_unknown":{"arn":true}}},
			{"address":"aws_ecs_service.app","mode":"managed","type":"aws_ecs_service","name":"app","change":{"actions":["create"],"after":{"region":"us-east-1"},"after_unknown":{"task_definition":true}}},
			{"address":"aws_subnet.a","mode":"managed","type":"aws_subnet","name":"a","change":{"actions":["create"],"after":{"region":"us-east-1"},"after_unknown":{}}},
			{"address":"aws_subnet.b","mode":"managed","type":"aws_subnet","name":"b","change":{"actions":["create"],"after":{"region":"us-east-1"},"after_unknown":{}}},
			{"address":"aws_lb_target_group.web[0]","mode":"managed","type":"aws_lb_target_group","name":"web","index":0,"change":{"actions":["create"],"after":{"region":"us-east-1"},"after_unknown":{}}},
			{"address":"aws_lb_target_group.web[1]","mode":"managed","type":"aws_lb_target_group","name":"web","index":1,"change":{"actions":["create"],"after":{"region":"us-east-1"},"after_unknown":{}}},
			{"address":"aws_sqs_queue.many[0]","mode":"managed","type":"aws_sqs_queue","name":"many","index":0,"change":{"actions":["create"],"after":{"region":"us-east-1"},"after_unknown":{}}},
			{"address":"aws_sqs_queue.many[1]","mode":"managed","type":"aws_sqs_queue","name":"many","index":1,"change":{"actions":["create"],"after":{"region":"us-east-1"},"after_unknown":{}}},
			{"address":"module.db.aws_rds_cluster.main","module_address":"module.db","mode":"managed","type":"aws_rds_cluster","name":"main","change":{"actions":["create"],"after":{"region":"us-east-1"},"after_unknown":{}}},
			{"address":"module.db.aws_rds_cluster_instance.main","module_address":"module.db","mode":"managed","type":"aws_rds_cluster_instance","name":"main","change":{"actions":["create"],"after":{"region":"us-east-1"},"after_unknown":{"cluster_identifier":true}}}]}`)
	resources := byAddress(set)
	want := map[string]map[string]string{
		"aws_ecs_service.app": {
			"task_definition":                "aws_ecs_task_definition.app",
			"load_balancer.target_group_arn": "aws_lb_target_group.web[1]",
		},
		"module.db.aws_rds_cluster_instance.main": {"cluster_identifier": "module.db.aws_rds_cluster.main"},
	}
	for id, resource := range resources {
		if !maps.Equal(resource.GetReferences(), want[id]) {
			t.Errorf("%s references = %v, want %v", id, resource.GetReferences(), want[id])
		}
	}
}

func TestAPlanReferenceThroughAModuleVariableResolvesByTheValueItNames(t *testing.T) {
	set, prior := plan(t, `{"format_version":"1.2",
		"resource_changes":[
			{"address":"module.task.aws_ecs_task_definition.app","module_address":"module.task","mode":"managed","type":"aws_ecs_task_definition","name":"app","change":{"actions":["no-op"],
				"after":{"region":"eu-west-1","arn":"arn:aws:ecs:eu-west-1:1:task-definition/app:3","id":"app","cpu":"1024"},"after_unknown":{}}},
			{"address":"module.svc.aws_ecs_service.app","module_address":"module.svc","mode":"managed","type":"aws_ecs_service","name":"app","change":{"actions":["update"],
				"after":{"region":"eu-west-1","desired_count":3,"task_definition":"arn:aws:ecs:eu-west-1:1:task-definition/app:3"},"after_unknown":{}}}],
		"prior_state":{"format_version":"1.0","values":{"root_module":{"child_modules":[
			{"address":"module.task","resources":[{"address":"module.task.aws_ecs_task_definition.app","mode":"managed","type":"aws_ecs_task_definition","name":"app",
				"values":{"region":"eu-west-1","arn":"arn:aws:ecs:eu-west-1:1:task-definition/app:3","id":"app","cpu":"1024"}}]},
			{"address":"module.svc","resources":[{"address":"module.svc.aws_ecs_service.app","mode":"managed","type":"aws_ecs_service","name":"app",
				"values":{"region":"eu-west-1","desired_count":2,"task_definition":"arn:aws:ecs:eu-west-1:1:task-definition/app:3"}}]}]}}},
		"configuration":{"root_module":{"module_calls":{
			"task":{"module":{"resources":[{"address":"aws_ecs_task_definition.app","mode":"managed","type":"aws_ecs_task_definition","name":"app","expressions":{"cpu":{"constant_value":"1024"}}}]}},
			"svc":{"module":{"resources":[{"address":"aws_ecs_service.app","mode":"managed","type":"aws_ecs_service","name":"app","expressions":{"task_definition":{"references":["var.task_definition_arn"]}}}]}}}}}}`)
	want := map[string]string{"task_definition": "module.task.aws_ecs_task_definition.app"}
	for name, side := range map[string]*costv1.ResourceSet{"plan": set, "prior": prior} {
		if got := byAddress(side)["module.svc.aws_ecs_service.app"].GetReferences(); !maps.Equal(got, want) {
			t.Errorf("%s references = %v, want %v", name, got, want)
		}
	}
}

func TestAStateReferencesTheOneResourceWhoseARNOrWhoseIDTheAttributeNames(t *testing.T) {
	set, err := terraform.ReadState([]byte(`{"format_version":"1.0","values":{"root_module":{"resources":[
		{"address":"aws_ecs_task_definition.app","mode":"managed","type":"aws_ecs_task_definition","name":"app","values":{"id":"app","arn":"arn:aws:ecs:us-east-1:1:task-definition/app:3","family":"app"}},
		{"address":"aws_ecs_service.app","mode":"managed","type":"aws_ecs_service","name":"app","values":{"id":"arn:aws:ecs:us-east-1:1:service/c/app",
			"task_definition":"arn:aws:ecs:us-east-1:1:task-definition/app:3","network_configuration":[{"security_groups":["sg-1"]}],"name":"app",
			"tags":{"Name":"app"},"queue_name":"shared"}},
		{"address":"aws_sqs_queue.one","mode":"managed","type":"aws_sqs_queue","name":"one","values":{"id":"shared"}},
		{"address":"aws_sqs_queue.two","mode":"managed","type":"aws_sqs_queue","name":"two","values":{"id":"shared"}},
		{"address":"aws_security_group.sg","mode":"managed","type":"aws_security_group","name":"sg","values":{"id":"sg-1"}}],
		"child_modules":[{"address":"module.db","resources":[
			{"address":"module.db.aws_rds_cluster.main","mode":"managed","type":"aws_rds_cluster","name":"main","values":{"id":"db","cluster_identifier":"db"}},
			{"address":"module.db.aws_rds_cluster_instance.main[0]","mode":"managed","type":"aws_rds_cluster_instance","name":"main","index":0,"values":{"id":"db-1","identifier":"db-1","cluster_identifier":"db"}}]}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]string{
		"aws_ecs_service.app":                        {"task_definition": "aws_ecs_task_definition.app"},
		"module.db.aws_rds_cluster_instance.main[0]": {"cluster_identifier": "module.db.aws_rds_cluster.main"},
	}
	for id, resource := range byAddress(set) {
		if !maps.Equal(resource.GetReferences(), want[id]) {
			t.Errorf("%s references = %v, want %v", id, resource.GetReferences(), want[id])
		}
	}
}

func TestATypeNamesItsVendor(t *testing.T) {
	set, err := terraform.ReadState([]byte(`{"format_version":"1.0","values":{"root_module":{"resources":[
		{"address":"google_storage_bucket.b","mode":"managed","type":"google_storage_bucket","name":"b","values":{}},
		{"address":"cloudflare_workers_script.w","mode":"managed","type":"cloudflare_workers_script","name":"w","values":{}},
		{"address":"random_id.r","mode":"managed","type":"random_id","name":"r","values":{}}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, resource := range set.GetResources() {
		got = append(got, resource.GetVendor())
	}
	if want := []string{"gcp", "cloudflare", "random"}; !slices.Equal(got, want) {
		t.Errorf("vendors = %v, want %v", got, want)
	}
}

func TestABlockADynamicBlockWritesKeepsItsUnknowns(t *testing.T) {
	set, _ := plan(t, `{"format_version":"1.2","resource_changes":[
		{"address":"tls_cert_request.dynamic","mode":"managed","type":"tls_cert_request","name":"dynamic",
			"change":{"actions":["create"],"after":{"dns_names":null,"subject":[{"country":null,"organization":"a"}]},
			"after_unknown":{"id":true,"private_key_pem":true,"subject":[{"common_name":true}]},"after_sensitive":{"private_key_pem":true,"subject":[{}]}}},
		{"address":"tls_cert_request.single","mode":"managed","type":"tls_cert_request","name":"single",
			"change":{"actions":["create"],"after":{"subject":{"organization":"a"}},
			"after_unknown":{"subject":{"common_name":true}},"after_sensitive":{}}}
	],"configuration":{"root_module":{"resources":[
		{"address":"tls_cert_request.dynamic","mode":"managed","type":"tls_cert_request","name":"dynamic","expressions":{
			"private_key_pem":{"references":["tls_private_key.k.private_key_pem","tls_private_key.k"]}}},
		{"address":"tls_cert_request.single","mode":"managed","type":"tls_cert_request","name":"single","expressions":{}}
	]}}}`)
	resources := byAddress(set)
	for address, want := range map[string][]string{
		"tls_cert_request.dynamic": {"private_key_pem", "region", "subject.common_name"},
		"tls_cert_request.single":  {"region", "subject.common_name"},
	} {
		if got := resources[address].GetUnknown(); !slices.Equal(got, want) {
			t.Errorf("%s unknown = %v, want %v", address, got, want)
		}
	}
}

func TestAnUnknownTheConfigurationNeverSetIsTheProvidersDefaultNotUnknown(t *testing.T) {
	set, _ := plan(t, `{"format_version":"1.2","resource_changes":[
		{"address":"aws_lambda_function.fn","mode":"managed","type":"aws_lambda_function","name":"fn",
			"change":{"actions":["create"],"after":{"region":"us-east-1","function_name":"fn","memory_size":512,"runtime":"nodejs20.x","tracing_config":[{}]},
			"after_unknown":{"architectures":true,"arn":true,"role":true,"tracing_config":[{"mode":true}],"logging_config":true},"after_sensitive":{}}},
		{"address":"google_cloud_run_v2_service.web","mode":"managed","type":"google_cloud_run_v2_service","name":"web",
			"change":{"actions":["create"],"after":{"location":"europe-west1","name":"web","template":[{"containers":[{"resources":[{"limits":{"cpu":"1"}}]}],"scaling":[{"min_instance_count":0}]}]},
			"after_unknown":{"ingress":true,"uri":true,"template":[{"service_account":true,"containers":[{"image":true,"ports":true,"resources":[{"limits":{}}]}],"scaling":[{"max_instance_count":true}]}]},"after_sensitive":{}}},
		{"address":"aws_sqs_queue.unconfigured","mode":"managed","type":"aws_sqs_queue","name":"unconfigured",
			"change":{"actions":["create"],"after":{"region":"us-east-1"},"after_unknown":{"fifo_queue":true},"after_sensitive":{}}}
	],"configuration":{"root_module":{"resources":[
		{"address":"aws_lambda_function.fn","mode":"managed","type":"aws_lambda_function","name":"fn","expressions":{
			"function_name":{"constant_value":"fn"},"memory_size":{"constant_value":512},"runtime":{"constant_value":"nodejs20.x"},
			"role":{"references":["aws_iam_role.fn.arn","aws_iam_role.fn"]},"tracing_config":[{"mode":{"references":["var.tracing"]}}]}},
		{"address":"google_cloud_run_v2_service.web","mode":"managed","type":"google_cloud_run_v2_service","name":"web","expressions":{
			"location":{"constant_value":"europe-west1"},"name":{"constant_value":"web"},
			"template":[{"containers":[{"image":{"references":["var.image"]},"resources":[{"limits":{"constant_value":{"cpu":"1"}}}]}],
				"scaling":[{"min_instance_count":{"constant_value":0}}]}]}}
	]}}}`)
	resources := byAddress(set)
	for address, want := range map[string][]string{
		"aws_lambda_function.fn":          {"role", "tracing_config.mode"},
		"google_cloud_run_v2_service.web": {"template.containers.image"},
		"aws_sqs_queue.unconfigured":      {"fifo_queue"},
	} {
		if got := resources[address].GetUnknown(); !slices.Equal(got, want) {
			t.Errorf("%s unknown = %v, want %v", address, got, want)
		}
	}
	if _, carried := resources["aws_lambda_function.fn"].GetProperties().GetFields()["architectures"]; carried {
		t.Error("architectures was carried, want it absent so the rule's default applies")
	}
}
