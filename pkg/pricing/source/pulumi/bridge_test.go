package pulumi_test

import (
	"context"
	"maps"
	"os"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/pricing/pricingtest"
	"github.com/ocelhq/ocel/pkg/pricing/ruleset"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
)

const (
	taskURN     = "urn:pulumi:dev::p::aws:ecs/taskDefinition:TaskDefinition::task"
	serviceURN  = "urn:pulumi:dev::p::aws:ecs/service:Service::service"
	alarmURN    = "urn:pulumi:dev::p::aws:cloudwatch/metricAlarm:MetricAlarm::alarm"
	databaseURN = "urn:pulumi:dev::p::gcp:sql/databaseInstance:DatabaseInstance::database"
	groupURN    = "urn:pulumi:dev::p::aws:lb/targetGroup:TargetGroup::group"
)

func renamedPreview(t *testing.T) *costv1.ResourceSet {
	t.Helper()
	set, _ := preview(t, `{"steps":[
		{"op":"create","urn":"`+groupURN+`","newState":{"urn":"`+groupURN+`","custom":true,"type":"aws:lb/targetGroup:TargetGroup","inputs":{"region":"us-east-1"}}},
		{"op":"create","urn":"`+taskURN+`","newState":{"urn":"`+taskURN+`","custom":true,"type":"aws:ecs/taskDefinition:TaskDefinition",
			"inputs":{"region":"us-east-1","cpu":"512","memory":"1024","requiresCompatibilities":["FARGATE"]}}},
		{"op":"create","urn":"`+serviceURN+`","newState":{"urn":"`+serviceURN+`","custom":true,"type":"aws:ecs/service:Service",
			"inputs":{"region":"us-east-1","desiredCount":5,"taskDefinition":"arn:task",
				"capacityProviderStrategies":[{"capacityProvider":"FARGATE","weight":1,"base":1},{"capacityProvider":"FARGATE_SPOT","weight":3}],
				"loadBalancers":[{"targetGroupArn":"04da6b54-80e4-46f7-96ec-b56ff0331ba9","containerPort":80}]},
			"propertyDependencies":{"taskDefinition":["`+taskURN+`"],"loadBalancers":["`+groupURN+`"]}}},
		{"op":"create","urn":"`+alarmURN+`","newState":{"urn":"`+alarmURN+`","custom":true,"type":"aws:cloudwatch/metricAlarm:MetricAlarm",
			"inputs":{"region":"us-east-1","name":"errors","metricQueries":[
				{"id":"a","metric":{"metricName":"Errors","namespace":"AWS/Lambda","period":60,"stat":"Sum"}},
				{"id":"b","metric":{"metricName":"Invocations","namespace":"AWS/Lambda","period":60,"stat":"Sum"}},
				{"id":"rate","expression":"a / b","returnData":true}]}}},
		{"op":"create","urn":"`+databaseURN+`","newState":{"urn":"`+databaseURN+`","custom":true,"type":"gcp:sql/databaseInstance:DatabaseInstance",
			"inputs":{"region":"europe-west1","databaseVersion":"POSTGRES_17","settings":{"tier":"db-custom-2-7680","edition":"ENTERPRISE",
				"availabilityType":"REGIONAL","diskType":"PD_SSD","diskSize":10,"ipConfiguration":{"pscConfigs":[{"pscEnabled":true}]}}}}}
	]}`)
	return set
}

func named(set *costv1.ResourceSet, name string) *costv1.Resource {
	for _, resource := range set.GetResources() {
		if resource.GetName() == name {
			return resource
		}
	}
	return nil
}

func TestAPropertyTheBridgeRenamedReadsUnderItsTerraformName(t *testing.T) {
	set := renamedPreview(t)

	service := named(set, "service").GetProperties().GetFields()
	if strategy := service["capacity_provider_strategy"].GetListValue().GetValues(); len(strategy) != 2 ||
		strategy[1].GetStructValue().GetFields()["capacity_provider"].GetStringValue() != "FARGATE_SPOT" {
		t.Errorf("service = %v, want capacityProviderStrategies as capacity_provider_strategy", service)
	}
	if want := []string{"load_balancer.target_group_arn"}; !slices.Equal(named(set, "service").GetUnknown(), want) {
		t.Errorf("service unknown = %v, want %v", named(set, "service").GetUnknown(), want)
	}
	if want := map[string]string{"task_definition": taskURN, "load_balancer": groupURN}; !maps.Equal(named(set, "service").GetReferences(), want) {
		t.Errorf("service references = %v, want %v", named(set, "service").GetReferences(), want)
	}
	alarm := named(set, "alarm").GetProperties().GetFields()
	if len(alarm["metric_query"].GetListValue().GetValues()) != 3 || alarm["alarm_name"].GetStringValue() != "errors" {
		t.Errorf("alarm = %v, want metricQueries as metric_query and name as alarm_name", alarm)
	}
	settings := named(set, "database").GetProperties().GetFields()["settings"].GetStructValue().GetFields()
	psc := settings["ip_configuration"].GetStructValue().GetFields()["psc_config"].GetStructValue().GetFields()
	if !psc["psc_enabled"].GetBoolValue() {
		t.Errorf("settings = %v, want pscConfigs as psc_config", settings)
	}
}

func catalogs(t *testing.T) *pricingtest.Card {
	t.Helper()
	var raw [][]byte
	for _, path := range []string{"../../aws/testdata/catalog.json", "../../gcp/testdata/catalog.json"} {
		held, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		raw = append(raw, held)
	}
	card, err := pricingtest.NewCard(raw...)
	if err != nil {
		t.Fatal(err)
	}
	return card
}

func TestAPreviewPricesThePropertiesTheBridgeRenamed(t *testing.T) {
	est, err := pricing.Estimate(context.Background(), catalogs(t), ruleset.Table(), renamedPreview(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	components := map[string]map[string]*costv1.CostComponent{}
	for _, resource := range est.GetResources() {
		components[resource.GetResource()] = map[string]*costv1.CostComponent{}
		for _, c := range resource.GetComponents() {
			components[resource.GetResource()][c.GetName()] = c
		}
	}
	if vcpu := components[serviceURN]["vCPU"]; vcpu.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_PRICED || vcpu.GetMonthlyQuantity() != "730" {
		t.Errorf("vCPU = %v, want the base task and 1 of the 4 weighted ones priced at half a vCPU for 730 hours", vcpu)
	}
	if metrics := components[alarmURN]["Alarm metrics"]; metrics.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_PRICED || metrics.GetMonthlyQuantity() != "2" {
		t.Errorf("alarm metrics = %v, want the 2 queries that read a metric priced", metrics)
	}
	if endpoint := components[databaseURN]["Private Service Connect endpoint"]; endpoint.GetPriceStatus() != costv1.CostComponent_PRICE_STATUS_PRICED {
		t.Errorf("endpoint = %v, want it priced where pscConfigs enables PSC", endpoint)
	}
}
