package pulumi_test

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/pricing/source/internal/golden"
	"github.com/ocelhq/ocel/pkg/pricing/source/pulumi"
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
	for _, name := range []string{"preview-create", "preview-update"} {
		t.Run(name, func(t *testing.T) {
			set, prior, _, err := pulumi.ReadPreview(fixture(t, name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			golden.Match(t, name, set, prior)
		})
	}
	t.Run("sst-diff", func(t *testing.T) {
		set, prior, _, err := pulumi.ReadSteps(fixture(t, "sst-diff.json"))
		if err != nil {
			t.Fatal(err)
		}
		golden.Match(t, "sst-diff", set, prior)
	})
	for _, name := range []string{"export", "sst-export"} {
		t.Run(name, func(t *testing.T) {
			set, err := pulumi.ReadExport(fixture(t, name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			golden.Match(t, name, set, nil)
		})
	}
}

func preview(t *testing.T, doc string) (*costv1.ResourceSet, *costv1.ResourceSet) {
	t.Helper()
	set, prior, _, err := pulumi.ReadPreview([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return set, prior
}

func only(t *testing.T, set *costv1.ResourceSet) *costv1.Resource {
	t.Helper()
	if len(set.GetResources()) != 1 {
		t.Fatalf("want one resource, got %v", set.GetResources())
	}
	return set.GetResources()[0]
}

func TestASecretIsDroppedAndListedUnknown(t *testing.T) {
	set, _ := preview(t, `{"steps":[{"op":"create","urn":"urn:pulumi:dev::p::aws:rds/cluster:Cluster::db","newState":{
		"urn":"urn:pulumi:dev::p::aws:rds/cluster:Cluster::db","custom":true,"type":"aws:rds/cluster:Cluster",
		"inputs":{"region":"eu-west-1","masterPassword":"[secret]",
			"masterUserSecret":{"4dabf18193072939515e22adb298388d":"1b47061264138c4ac30d75fd1eb44270","ciphertext":"v1:abc"},
			"engine":"aurora-postgresql"}}}]}`)
	resource := only(t, set)
	fields := resource.GetProperties().GetFields()
	if _, carried := fields["master_password"]; carried {
		t.Error("the masked secret was carried")
	}
	if _, carried := fields["master_user_secret"]; carried {
		t.Error("the encrypted secret was carried")
	}
	if fields["engine"].GetStringValue() != "aurora-postgresql" {
		t.Errorf("engine = %v", fields["engine"])
	}
	if want := []string{"master_password", "master_user_secret"}; !slices.Equal(resource.GetUnknown(), want) {
		t.Errorf("unknown = %v, want %v", resource.GetUnknown(), want)
	}
}

func TestAnUnknownOutputKeepsTheKnownInputAndAnUnknownInputStaysUnknown(t *testing.T) {
	set, _ := preview(t, `{"steps":[{"op":"update","urn":"urn:pulumi:dev::p::aws:lambda/function:Function::fn",
		"oldState":{"urn":"urn:pulumi:dev::p::aws:lambda/function:Function::fn","custom":true,"type":"aws:lambda/function:Function",
			"inputs":{"memorySize":512,"region":"us-east-1"},"outputs":{"memorySize":512,"region":"us-east-1","arn":"arn:1"}},
		"newState":{"urn":"urn:pulumi:dev::p::aws:lambda/function:Function::fn","custom":true,"type":"aws:lambda/function:Function",
			"inputs":{"memorySize":1024,"region":"us-east-1","role":"04da6b54-80e4-46f7-96ec-b56ff0331ba9","__defaults":[]},
			"outputs":{"memorySize":"04da6b54-80e4-46f7-96ec-b56ff0331ba9","region":"us-east-1",
				"role":"04da6b54-80e4-46f7-96ec-b56ff0331ba9","tags":null,"__meta":"{}"}}}]}`)
	resource := only(t, set)
	fields := resource.GetProperties().GetFields()
	if fields["memory_size"].GetNumberValue() != 1024 {
		t.Errorf("memory_size = %v, want the input 1024", fields["memory_size"])
	}
	for _, dropped := range []string{"__defaults", "__meta", "tags", "role"} {
		if _, carried := fields[dropped]; carried {
			t.Errorf("%s was carried", dropped)
		}
	}
	if want := []string{"role"}; !slices.Equal(resource.GetUnknown(), want) {
		t.Errorf("unknown = %v, want %v", resource.GetUnknown(), want)
	}
}

func TestAnUnknownOutputWithNoInputIsTheProvidersDefaultNotUnknown(t *testing.T) {
	set, _ := preview(t, `{"steps":[
		{"op":"create","urn":"urn:pulumi:dev::p::aws:lambda/function:Function::fn","newState":{
			"urn":"urn:pulumi:dev::p::aws:lambda/function:Function::fn","custom":true,"type":"aws:lambda/function:Function",
			"inputs":{"region":"us-east-1","memorySize":512,"role":"04da6b54-80e4-46f7-96ec-b56ff0331ba9","loggingConfig":{"logFormat":"JSON"}},
			"outputs":{"region":"us-east-1","memorySize":512,"role":"04da6b54-80e4-46f7-96ec-b56ff0331ba9",
				"architectures":"04da6b54-80e4-46f7-96ec-b56ff0331ba9","arn":"04da6b54-80e4-46f7-96ec-b56ff0331ba9",
				"loggingConfig":{"logFormat":"JSON","logGroup":"04da6b54-80e4-46f7-96ec-b56ff0331ba9"}}}},
		{"op":"create","urn":"urn:pulumi:dev::p::gcp:cloudrunv2/service:Service::web","newState":{
			"urn":"urn:pulumi:dev::p::gcp:cloudrunv2/service:Service::web","custom":true,"type":"gcp:cloudrunv2/service:Service",
			"inputs":{"location":"europe-west1","template":{"containers":[{"image":"04da6b54-80e4-46f7-96ec-b56ff0331ba9"}]}},
			"outputs":{"location":"europe-west1","ingress":"04da6b54-80e4-46f7-96ec-b56ff0331ba9","uri":"04da6b54-80e4-46f7-96ec-b56ff0331ba9",
				"template":{"serviceAccount":"04da6b54-80e4-46f7-96ec-b56ff0331ba9",
					"containers":[{"image":"04da6b54-80e4-46f7-96ec-b56ff0331ba9","name":"04da6b54-80e4-46f7-96ec-b56ff0331ba9"}]}}}}
	]}`)
	want := map[string][]string{"fn": {"role"}, "web": {"template.containers.image"}}
	for _, resource := range set.GetResources() {
		if got := resource.GetUnknown(); !slices.Equal(got, want[resource.GetName()]) {
			t.Errorf("%s unknown = %v, want %v", resource.GetName(), got, want[resource.GetName()])
		}
	}
	if logging := named(set, "fn").GetProperties().GetFields()["logging_config"].GetStructValue().GetFields(); logging["log_format"].GetStringValue() != "JSON" {
		t.Errorf("logging_config = %v, want the configured log_format kept", logging)
	}
}

func TestAnUnknownElementMarksTheWholeListUnknown(t *testing.T) {
	set, _ := preview(t, `{"steps":[{"op":"create","urn":"urn:pulumi:dev::p::aws:ec2/instance:Instance::vm","newState":{
		"urn":"urn:pulumi:dev::p::aws:ec2/instance:Instance::vm","custom":true,"type":"aws:ec2/instance:Instance",
		"inputs":{"region":"us-east-1","vpcSecurityGroupIds":["sg-1","04da6b54-80e4-46f7-96ec-b56ff0331ba9"],
			"ebsBlockDevices":[{"volumeSize":8},{"volumeSize":"04da6b54-80e4-46f7-96ec-b56ff0331ba9"}]}}}]}`)
	resource := only(t, set)
	if want := []string{"ebs_block_devices.1.volume_size", "vpc_security_group_ids"}; !slices.Equal(resource.GetUnknown(), want) {
		t.Errorf("unknown = %v, want %v", resource.GetUnknown(), want)
	}
}

func TestAListHoldingOneObjectFlattensToThatObjectAndItsUnknownPathsWithIt(t *testing.T) {
	set, _ := preview(t, `{"steps":[{"op":"create","urn":"urn:pulumi:dev::p::gcp:cloudrunv2/service:Service::web","newState":{
		"urn":"urn:pulumi:dev::p::gcp:cloudrunv2/service:Service::web","custom":true,"type":"gcp:cloudrunv2/service:Service",
		"inputs":{"location":"europe-west1","template":{"containers":[{"resources":{"cpuIdle":"04da6b54-80e4-46f7-96ec-b56ff0331ba9","limits":{"cpu":"1"}}}],
			"volumes":[{"name":"a"},{"name":"04da6b54-80e4-46f7-96ec-b56ff0331ba9"}]}}}}]}`)
	resource := only(t, set)
	containers := resource.GetProperties().GetFields()["template"].GetStructValue().GetFields()["containers"]
	if containers.GetStructValue().GetFields()["resources"].GetStructValue().GetFields()["limits"].GetStructValue().GetFields()["cpu"].GetStringValue() != "1" {
		t.Errorf("containers = %v, want the one container as an object", containers)
	}
	if want := []string{"template.containers.resources.cpu_idle", "template.volumes.1.name"}; !slices.Equal(resource.GetUnknown(), want) {
		t.Errorf("unknown = %v, want %v", resource.GetUnknown(), want)
	}
}

func TestAnInputThatDependsOnOneCustomResourceReferencesIt(t *testing.T) {
	const (
		cluster    = "urn:pulumi:dev::p::aws:rds/cluster:Cluster::db"
		instance   = "urn:pulumi:dev::p::aws:rds/clusterInstance:ClusterInstance::db-1"
		taskDef    = "urn:pulumi:dev::p::my:index:Svc$aws:ecs/taskDefinition:TaskDefinition::td"
		service    = "urn:pulumi:dev::p::my:index:Svc$aws:ecs/service:Service::svc"
		component  = "urn:pulumi:dev::p::my:index:Svc::svc"
		subnetA    = "urn:pulumi:dev::p::aws:ec2/subnet:Subnet::a"
		subnetB    = "urn:pulumi:dev::p::aws:ec2/subnet:Subnet::b"
		notPreview = "urn:pulumi:dev::p::aws:ecs/cluster:Cluster::omitted"
	)
	state := func(urn, typ, parent, deps string) string {
		return `{"op":"create","urn":"` + urn + `","newState":{"urn":"` + urn + `","custom":true,"type":"` + typ + `","parent":"` + parent +
			`","inputs":{"region":"us-east-1"},"propertyDependencies":` + deps + `}}`
	}
	set, _ := preview(t, `{"steps":[
		{"op":"create","urn":"`+component+`","newState":{"urn":"`+component+`","type":"my:index:Svc"}},
		`+state(cluster, "aws:rds/cluster:Cluster", "", `{}`)+`,
		`+state(instance, "aws:rds/clusterInstance:ClusterInstance", "", `{"clusterIdentifier":["`+cluster+`"],"engine":[],"region":[]}`)+`,
		`+state(subnetA, "aws:ec2/subnet:Subnet", "", `{}`)+`,
		`+state(subnetB, "aws:ec2/subnet:Subnet", "", `{}`)+`,
		`+state(service, "aws:ecs/service:Service", component, `{"taskDefinition":["`+component+`","`+taskDef+`"],
			"networkConfiguration":["`+subnetA+`","`+subnetB+`"],"cluster":["`+notPreview+`"],"iamRole":["`+component+`"]}`)+`,
		`+state(taskDef, "aws:ecs/taskDefinition:TaskDefinition", component, `{}`)+`
	]}`)
	want := map[string]map[string]string{
		"db-1": {"cluster_identifier": cluster},
		"svc":  {"task_definition": taskDef},
	}
	for _, resource := range set.GetResources() {
		if !maps.Equal(resource.GetReferences(), want[resource.GetName()]) {
			t.Errorf("%s references = %v, want %v", resource.GetName(), resource.GetReferences(), want[resource.GetName()])
		}
	}
}

func TestAnExportReferencesThroughPropertyDependenciesToo(t *testing.T) {
	set, err := pulumi.ReadExport([]byte(`{"deployment":{"resources":[
		{"urn":"urn:pulumi:dev::p::aws:rds/clusterInstance:ClusterInstance::db-1","custom":true,"type":"aws:rds/clusterInstance:ClusterInstance",
			"inputs":{"region":"us-east-1"},"propertyDependencies":{"clusterIdentifier":["urn:pulumi:dev::p::aws:rds/cluster:Cluster::db"]}},
		{"urn":"urn:pulumi:dev::p::aws:rds/cluster:Cluster::db","custom":true,"type":"aws:rds/cluster:Cluster","inputs":{"region":"us-east-1"}}
	]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := set.GetResources()[0].GetReferences(); !maps.Equal(got, map[string]string{"cluster_identifier": "urn:pulumi:dev::p::aws:rds/cluster:Cluster::db"}) {
		t.Errorf("references = %v", got)
	}
}

func TestMapKeysKeepTheirSpelling(t *testing.T) {
	set, _ := preview(t, `{"steps":[{"op":"create","urn":"urn:pulumi:dev::p::aws:lambda/function:Function::fn","newState":{
		"urn":"urn:pulumi:dev::p::aws:lambda/function:Function::fn","custom":true,"type":"aws:lambda/function:Function",
		"inputs":{"region":"us-east-1","environment":{"variables":{"SST_RESOURCE_App":"x"}},"tags":{"costCenter":"42"}}}}]}`)
	resource := only(t, set)
	variables := resource.GetProperties().GetFields()["environment"].GetStructValue().GetFields()["variables"].GetStructValue().GetFields()
	if _, kept := variables["SST_RESOURCE_App"]; !kept {
		t.Errorf("variables = %v, want the key spelled as written", variables)
	}
	if resource.GetTags()["costCenter"] != "42" {
		t.Errorf("tags = %v", resource.GetTags())
	}
}

func TestRegionResolvesFromTheResourceThenItsProviderThenStackConfig(t *testing.T) {
	set, _ := preview(t, `{"config":{"aws:region":"eu-west-2","gcp:region":"us-central1"},"steps":[
		{"op":"same","urn":"urn:pulumi:dev::p::pulumi:providers:aws::east","newState":{"urn":"urn:pulumi:dev::p::pulumi:providers:aws::east","custom":true,"type":"pulumi:providers:aws","id":"1","inputs":{"region":"us-east-1"}}},
		{"op":"same","urn":"urn:pulumi:dev::p::pulumi:providers:gcp::zoned","newState":{"urn":"urn:pulumi:dev::p::pulumi:providers:gcp::zoned","custom":true,"type":"pulumi:providers:gcp","id":"2","inputs":{"zone":"europe-west1-b"}}},
		{"op":"same","urn":"urn:pulumi:dev::p::pulumi:providers:aws::hidden","newState":{"urn":"urn:pulumi:dev::p::pulumi:providers:aws::hidden","custom":true,"type":"pulumi:providers:aws","id":"3","inputs":{"region":"04da6b54-80e4-46f7-96ec-b56ff0331ba9"}}},
		{"op":"create","urn":"urn:pulumi:dev::p::aws:sqs/queue:Queue::own","newState":{"urn":"urn:pulumi:dev::p::aws:sqs/queue:Queue::own","custom":true,"type":"aws:sqs/queue:Queue","provider":"urn:pulumi:dev::p::pulumi:providers:aws::east::1","inputs":{"region":"ap-south-1"}}},
		{"op":"create","urn":"urn:pulumi:dev::p::aws:sqs/queue:Queue::east","newState":{"urn":"urn:pulumi:dev::p::aws:sqs/queue:Queue::east","custom":true,"type":"aws:sqs/queue:Queue","provider":"urn:pulumi:dev::p::pulumi:providers:aws::east::1","inputs":{}}},
		{"op":"create","urn":"urn:pulumi:dev::p::gcp:pubsub/topic:Topic::zoned","newState":{"urn":"urn:pulumi:dev::p::gcp:pubsub/topic:Topic::zoned","custom":true,"type":"gcp:pubsub/topic:Topic","provider":"urn:pulumi:dev::p::pulumi:providers:gcp::zoned::2","inputs":{}}},
		{"op":"create","urn":"urn:pulumi:dev::p::aws:sqs/queue:Queue::hidden","newState":{"urn":"urn:pulumi:dev::p::aws:sqs/queue:Queue::hidden","custom":true,"type":"aws:sqs/queue:Queue","provider":"urn:pulumi:dev::p::pulumi:providers:aws::hidden::3","inputs":{}}},
		{"op":"create","urn":"urn:pulumi:dev::p::aws:sqs/queue:Queue::configured","newState":{"urn":"urn:pulumi:dev::p::aws:sqs/queue:Queue::configured","custom":true,"type":"aws:sqs/queue:Queue","provider":"urn:pulumi:dev::p::pulumi:providers:aws::default_7_36_0::04da6b54-80e4-46f7-96ec-b56ff0331ba9","inputs":{}}},
		{"op":"create","urn":"urn:pulumi:dev::p::cloudflare:index/workersScript:WorkersScript::bare","newState":{"urn":"urn:pulumi:dev::p::cloudflare:index/workersScript:WorkersScript::bare","custom":true,"type":"cloudflare:index/workersScript:WorkersScript","inputs":{}}}
	]}`)
	want := map[string]string{"own": "ap-south-1", "east": "us-east-1", "zoned": "europe-west1", "hidden": "", "configured": "eu-west-2", "bare": ""}
	for _, resource := range set.GetResources() {
		if resource.GetRegion() != want[resource.GetName()] {
			t.Errorf("%s region = %q, want %q", resource.GetName(), resource.GetRegion(), want[resource.GetName()])
		}
		unknown := slices.Contains(resource.GetUnknown(), "region")
		if unknown != (want[resource.GetName()] == "") {
			t.Errorf("%s unknown = %v", resource.GetName(), resource.GetUnknown())
		}
	}
	if len(set.GetResources()) != len(want) {
		t.Errorf("got %d resources, want %d", len(set.GetResources()), len(want))
	}
}

func TestAGCPResourceTakesItsRegionFromItsLocationBeforeItsProvider(t *testing.T) {
	set, _ := preview(t, `{"config":{"aws:region":"eu-west-2"},"steps":[
		{"op":"same","urn":"urn:pulumi:dev::p::pulumi:providers:gcp::zoned","newState":{"urn":"urn:pulumi:dev::p::pulumi:providers:gcp::zoned","custom":true,"type":"pulumi:providers:gcp","id":"2","inputs":{"zone":"europe-west1-b"}}},
		{"op":"create","urn":"urn:pulumi:dev::p::gcp:cloudrunv2/service:Service::located","newState":{"urn":"urn:pulumi:dev::p::gcp:cloudrunv2/service:Service::located","custom":true,"type":"gcp:cloudrunv2/service:Service","provider":"urn:pulumi:dev::p::pulumi:providers:gcp::zoned::2","inputs":{"location":"europe-west4"}}},
		{"op":"create","urn":"urn:pulumi:dev::p::gcp:storage/bucket:Bucket::multi","newState":{"urn":"urn:pulumi:dev::p::gcp:storage/bucket:Bucket::multi","custom":true,"type":"gcp:storage/bucket:Bucket","provider":"urn:pulumi:dev::p::pulumi:providers:gcp::zoned::2","inputs":{"location":"EU"}}},
		{"op":"create","urn":"urn:pulumi:dev::p::aws:sqs/queue:Queue::aws","newState":{"urn":"urn:pulumi:dev::p::aws:sqs/queue:Queue::aws","custom":true,"type":"aws:sqs/queue:Queue","inputs":{"location":"us-east-1"}}}
	]}`)
	want := map[string]string{"located": "europe-west4", "multi": "", "aws": "eu-west-2"}
	for _, resource := range set.GetResources() {
		if resource.GetRegion() != want[resource.GetName()] {
			t.Errorf("%s region = %q, want %q", resource.GetName(), resource.GetRegion(), want[resource.GetName()])
		}
		if slices.Contains(resource.GetUnknown(), "region") != (want[resource.GetName()] == "") {
			t.Errorf("%s unknown = %v", resource.GetName(), resource.GetUnknown())
		}
	}
}

func TestADeleteGoesToThePriorOnlyAndAReplaceToBoth(t *testing.T) {
	set, prior := preview(t, `{"steps":[
		{"op":"delete","urn":"urn:pulumi:dev::p::aws:sqs/queue:Queue::gone","oldState":{"urn":"urn:pulumi:dev::p::aws:sqs/queue:Queue::gone","custom":true,"type":"aws:sqs/queue:Queue","inputs":{"region":"us-east-1"}}},
		{"op":"replace","urn":"urn:pulumi:dev::p::aws:sqs/queue:Queue::swapped",
			"oldState":{"urn":"urn:pulumi:dev::p::aws:sqs/queue:Queue::swapped","custom":true,"type":"aws:sqs/queue:Queue","inputs":{"region":"us-east-1","fifoQueue":false}},
			"newState":{"urn":"urn:pulumi:dev::p::aws:sqs/queue:Queue::swapped","custom":true,"type":"aws:sqs/queue:Queue","inputs":{"region":"us-east-1","fifoQueue":true}}},
		{"op":"create","urn":"urn:pulumi:dev::p::aws:sqs/queue:Queue::fresh","newState":{"urn":"urn:pulumi:dev::p::aws:sqs/queue:Queue::fresh","custom":true,"type":"aws:sqs/queue:Queue","inputs":{"region":"us-east-1"}}}
	]}`)
	if got := names(set); !slices.Equal(got, []string{"swapped", "fresh"}) {
		t.Errorf("set = %v", got)
	}
	if got := names(prior); !slices.Equal(got, []string{"gone", "swapped"}) {
		t.Errorf("prior = %v", got)
	}
	if !set.GetResources()[0].GetProperties().GetFields()["fifo_queue"].GetBoolValue() {
		t.Error("the set carries the replaced state")
	}
}

func names(set *costv1.ResourceSet) []string {
	var out []string
	for _, resource := range set.GetResources() {
		out = append(out, resource.GetName())
	}
	return out
}

func TestAResourceScopesUnderItsComponentEvenWhenThePreviewOmitsIt(t *testing.T) {
	set, _ := preview(t, `{"steps":[{"op":"update","urn":"urn:pulumi:dev::p::my:index:Api$aws:sqs/queue:Queue::q","newState":{
		"urn":"urn:pulumi:dev::p::my:index:Api$aws:sqs/queue:Queue::q","custom":true,"type":"aws:sqs/queue:Queue",
		"parent":"urn:pulumi:dev::p::my:index:Api::api","inputs":{"region":"us-east-1"}}}]}`)
	resource := only(t, set)
	var component *costv1.Scope
	for _, scope := range set.GetScopes() {
		if scope.GetId() == resource.GetScope() {
			component = scope
		}
	}
	if component.GetKind() != "component" || component.GetName() != "api" || component.GetParent() != "stack:dev" {
		t.Errorf("scope = %v", component)
	}
}

func TestAParentChainThatNeverReachesTheStackIsRefused(t *testing.T) {
	_, err := pulumi.ReadExport([]byte(`{"deployment":{"resources":[
		{"urn":"urn:pulumi:dev::p::my:index:A::a","type":"my:index:A","parent":"urn:pulumi:dev::p::my:index:B::b"},
		{"urn":"urn:pulumi:dev::p::my:index:B::b","type":"my:index:B","parent":"urn:pulumi:dev::p::my:index:A::a"},
		{"urn":"urn:pulumi:dev::p::aws:sqs/queue:Queue::q","custom":true,"type":"aws:sqs/queue:Queue","parent":"urn:pulumi:dev::p::my:index:A::a","inputs":{}}
	]}}`))
	if err == nil || !strings.Contains(err.Error(), "never reaches the stack") {
		t.Errorf("err = %v", err)
	}
}

func TestAnSSTDocumentIsSourcedAsSSTWithAStageRoot(t *testing.T) {
	set, err := pulumi.ReadExport(fixture(t, "sst-export.json"))
	if err != nil {
		t.Fatal(err)
	}
	if set.GetSource() != "sst" || set.GetScopes()[0].GetKind() != "stage" || set.GetScopes()[0].GetName() != "production" {
		t.Errorf("source %q, root %v", set.GetSource(), set.GetScopes()[0])
	}
}

func TestAnSSTDiffIsSourcedAsSSTEvenWhenOnlyPlainResourcesChanged(t *testing.T) {
	set, prior, _, err := pulumi.ReadSteps([]byte(`[{"op":"update","urn":"urn:pulumi:production::shop::aws:sqs/queue:Queue::q",
		"old":{"urn":"urn:pulumi:production::shop::aws:sqs/queue:Queue::q","custom":true,"type":"aws:sqs/queue:Queue","inputs":{"region":"us-east-1"}},
		"new":{"urn":"urn:pulumi:production::shop::aws:sqs/queue:Queue::q","custom":true,"type":"aws:sqs/queue:Queue","inputs":{"region":"us-east-1"}}}]`))
	if err != nil {
		t.Fatal(err)
	}
	if set.GetSource() != "sst" || prior.GetSource() != "sst" || set.GetScopes()[0].GetKind() != "stage" {
		t.Errorf("set %v, prior %v", set, prior)
	}
}

func TestAPendingDeleteInAnExportIsNotInventory(t *testing.T) {
	set, err := pulumi.ReadExport([]byte(`{"deployment":{"resources":[
		{"urn":"urn:pulumi:dev::p::aws:sqs/queue:Queue::q","custom":true,"type":"aws:sqs/queue:Queue","inputs":{"region":"us-east-1"},"delete":true},
		{"urn":"urn:pulumi:dev::p::aws:sqs/queue:Queue::q","custom":true,"type":"aws:sqs/queue:Queue","inputs":{"region":"us-east-1"}}
	]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(set.GetResources()) != 1 {
		t.Errorf("resources = %v", set.GetResources())
	}
}

func TestAPreviewOfAnExistingStackWithoutUnchangedResourcesSaysItPricesOnlyTheChanges(t *testing.T) {
	sameStack := `{"op":"same","urn":"urn:pulumi:dev::p::pulumi:pulumi:Stack::p-dev","oldState":{"urn":"urn:pulumi:dev::p::pulumi:pulumi:Stack::p-dev","type":"pulumi:pulumi:Stack"},"newState":{"urn":"urn:pulumi:dev::p::pulumi:pulumi:Stack::p-dev","type":"pulumi:pulumi:Stack"}}`
	update := `{"op":"update","urn":"urn:pulumi:dev::p::aws:sqs/queue:Queue::q","oldState":{"urn":"urn:pulumi:dev::p::aws:sqs/queue:Queue::q","custom":true,"type":"aws:sqs/queue:Queue","inputs":{}},"newState":{"urn":"urn:pulumi:dev::p::aws:sqs/queue:Queue::q","custom":true,"type":"aws:sqs/queue:Queue","inputs":{}}}`
	sameQueue := `{"op":"same","urn":"urn:pulumi:dev::p::aws:sqs/queue:Queue::k","oldState":{"urn":"urn:pulumi:dev::p::aws:sqs/queue:Queue::k","custom":true,"type":"aws:sqs/queue:Queue","inputs":{}},"newState":{"urn":"urn:pulumi:dev::p::aws:sqs/queue:Queue::k","custom":true,"type":"aws:sqs/queue:Queue","inputs":{}}}`
	for _, c := range []struct {
		name, doc   string
		changesOnly bool
	}{
		{"without sames", `{"steps":[` + sameStack + `,` + update + `]}`, true},
		{"with sames", `{"steps":[` + sameStack + `,` + update + `,` + sameQueue + `]}`, false},
		{"fresh stack", string(fixture(t, "preview-create.json")), false},
		{"show-sames fixture", string(fixture(t, "preview-update.json")), false},
	} {
		_, _, assumptions, err := pulumi.ReadPreview([]byte(c.doc))
		if err != nil {
			t.Fatal(err)
		}
		if got := slices.Contains(assumptions, pulumi.AssumptionChangesOnly); got != c.changesOnly {
			t.Errorf("%s: assumptions = %v", c.name, assumptions)
		}
	}
}

func TestAnSSTDiffSaysItPricesOnlyTheChange(t *testing.T) {
	_, _, assumptions, err := pulumi.ReadSteps(fixture(t, "sst-diff.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(assumptions, []string{pulumi.AssumptionSSTDiff}) {
		t.Errorf("assumptions = %v", assumptions)
	}
}
