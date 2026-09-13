package pulumi_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/costkit/pulumi"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func parse(t *testing.T, name string, opts pulumi.Options) *costv1.ResourceSet {
	t.Helper()
	set, err := pulumi.Parse(fixture(t, name), opts)
	if err != nil {
		t.Fatalf("Parse(%s) = %v", name, err)
	}
	return set
}

func resource(t *testing.T, set *costv1.ResourceSet, urn string) *costv1.Resource {
	t.Helper()
	for _, held := range set.GetResources() {
		if held.GetId() == urn {
			return held
		}
	}
	var ids []string
	for _, held := range set.GetResources() {
		ids = append(ids, held.GetId())
	}
	t.Fatalf("no resource %s among %v", urn, ids)
	return nil
}

func TestPreviewNamesTheStackItWasTakenFrom(t *testing.T) {
	set := parse(t, "preview.json", pulumi.Options{Source: pulumi.SourcePulumi, Name: "dev"})

	if set.GetSource() != "pulumi" {
		t.Errorf("source = %q, want pulumi", set.GetSource())
	}
	root := set.GetScopes()[0]
	if root.GetParent() != "" || root.GetKind() != "stack" || root.GetName() != "dev" {
		t.Errorf("root scope = %v, want the stack dev at the root", root)
	}
}

func TestPreviewKeepsTheCustomResourcesAndDropsTheRest(t *testing.T) {
	set := parse(t, "preview.json", pulumi.Options{Source: pulumi.SourcePulumi, Name: "dev"})

	var ids []string
	for _, held := range set.GetResources() {
		ids = append(ids, held.GetId())
	}
	want := []string{
		"urn:pulumi:dev::shop::my:mod:Api$aws:lambda/function:Function::handler",
		"urn:pulumi:dev::shop::aws:s3/bucket:Bucket::assets",
		"urn:pulumi:dev::shop::aws:ec2/natGateway:NatGateway::nat",
		"urn:pulumi:dev::shop::random:index/randomPet:RandomPet::pet",
	}
	if len(ids) != len(want) {
		t.Fatalf("resources = %v, want %v", ids, want)
	}
	for i, id := range want {
		if ids[i] != id {
			t.Errorf("resource %d = %q, want %q", i, ids[i], id)
		}
	}

	fn := resource(t, set, want[0])
	if fn.GetVendor() != "aws" || fn.GetType() != "aws_lambda_function" || fn.GetName() != "handler" {
		t.Errorf("the function = vendor %q type %q name %q, want aws aws_lambda_function handler", fn.GetVendor(), fn.GetType(), fn.GetName())
	}
	pet := resource(t, set, want[3])
	if pet.GetVendor() != "random" || pet.GetType() != "random_pet" {
		t.Errorf("the pet = vendor %q type %q, want a random_pet coverage names", pet.GetVendor(), pet.GetType())
	}
}

func scopeOf(t *testing.T, set *costv1.ResourceSet, id string) *costv1.Scope {
	t.Helper()
	for _, held := range set.GetScopes() {
		if held.GetId() == id {
			return held
		}
	}
	var ids []string
	for _, held := range set.GetScopes() {
		ids = append(ids, held.GetId())
	}
	t.Fatalf("no scope %s among %v", id, ids)
	return nil
}

func TestComponentResourcesBecomeTheScopesTheirChildrenAreGroupedUnder(t *testing.T) {
	set := parse(t, "preview.json", pulumi.Options{Source: pulumi.SourcePulumi, Name: "dev"})

	root := "stack:dev"
	component := scopeOf(t, set, root+"/component:api")
	if component.GetParent() != root || component.GetKind() != "component" || component.GetName() != "api" {
		t.Errorf("the api scope = %v, want a component named api under the stack", component)
	}
	if got := resource(t, set, "urn:pulumi:dev::shop::my:mod:Api$aws:lambda/function:Function::handler").GetScope(); got != component.GetId() {
		t.Errorf("the function's scope = %q, want %q", got, component.GetId())
	}
	if got := resource(t, set, "urn:pulumi:dev::shop::aws:s3/bucket:Bucket::assets").GetScope(); got != root {
		t.Errorf("the bucket's scope = %q, want the stack itself", got)
	}
}

func TestTheProviderRecordDecidesTheRegionAndTheConfigIsTheFallback(t *testing.T) {
	set := parse(t, "preview.json", pulumi.Options{Source: pulumi.SourcePulumi, Name: "dev"})

	if got := resource(t, set, "urn:pulumi:dev::shop::aws:s3/bucket:Bucket::assets").GetRegion(); got != "eu-west-1" {
		t.Errorf("the bucket's region = %q, want the region of the provider it names", got)
	}
	if got := resource(t, set, "urn:pulumi:dev::shop::aws:ec2/natGateway:NatGateway::nat").GetRegion(); got != "us-east-1" {
		t.Errorf("the gateway's region = %q, want the stack config's aws:region", got)
	}
}

func TestInputsArriveAsSnakeCasePropertiesWithTheTagsBesideThem(t *testing.T) {
	set := parse(t, "preview.json", pulumi.Options{Source: pulumi.SourcePulumi, Name: "dev"})

	fn := resource(t, set, "urn:pulumi:dev::shop::my:mod:Api$aws:lambda/function:Function::handler")
	props := fn.GetProperties().AsMap()
	if props["memory_size"] != float64(512) {
		t.Errorf("memory_size = %v, want 512", props["memory_size"])
	}
	nested, _ := props["ephemeral_storage"].(map[string]any)
	if nested["size"] != float64(512) {
		t.Errorf("ephemeral_storage = %v, want a nested size of 512", props["ephemeral_storage"])
	}
	if fn.GetTags()["app"] != "shop" {
		t.Errorf("tags = %v, want the string map the inputs carry", fn.GetTags())
	}
}

func TestAnUnknownOrSecretInputIsNamedRatherThanGuessed(t *testing.T) {
	set := parse(t, "preview.json", pulumi.Options{Source: pulumi.SourcePulumi, Name: "dev"})

	nat := resource(t, set, "urn:pulumi:dev::shop::aws:ec2/natGateway:NatGateway::nat")
	want := []string{"allocation_id", "subnet_id"}
	if len(nat.GetUnknown()) != len(want) || nat.GetUnknown()[0] != want[0] || nat.GetUnknown()[1] != want[1] {
		t.Errorf("unknown = %v, want %v", nat.GetUnknown(), want)
	}
	props := nat.GetProperties().AsMap()
	if _, present := props["subnet_id"]; present {
		t.Errorf("properties = %v, want the unknown subnet_id left out rather than carrying a sentinel", props)
	}
	if _, present := props["allocation_id"]; present {
		t.Errorf("properties = %v, want the secret allocation_id left out", props)
	}
}

func TestACheckpointIsReadAsTheWholeStage(t *testing.T) {
	set := parse(t, "sst_state.json", pulumi.Options{Source: pulumi.SourceSST, Name: "victor"})

	if set.GetSource() != "sst" || set.GetScopes()[0].GetKind() != "stage" {
		t.Errorf("set = source %q root %v, want the sst stage", set.GetSource(), set.GetScopes()[0])
	}
	scopeOf(t, set, "stage:victor/component:Vpc")
	nat := resource(t, set, "urn:pulumi:victor::with-sst::sst:aws:Vpc$aws:ec2/natGateway:NatGateway::Vpc NatGateway1")
	if nat.GetType() != "aws_nat_gateway" || nat.GetRegion() != "us-east-1" {
		t.Errorf("the gateway = %v, want an aws_nat_gateway in us-east-1", nat)
	}
}

func TestTheDiffOverridesTheStateItWasTakenAgainst(t *testing.T) {
	set, err := pulumi.Merge(fixture(t, "sst_state.json"), fixture(t, "sst_diff.json"), pulumi.Options{Source: pulumi.SourceSST, Name: "victor"})
	if err != nil {
		t.Fatalf("Merge() = %v", err)
	}

	fn := resource(t, set, "urn:pulumi:victor::with-sst::sst:aws:Function$aws:lambda/function:Function::Api Function")
	if fn.GetProperties().AsMap()["memory_size"] != float64(2048) {
		t.Errorf("memory_size = %v, want the 2048 the diff carries", fn.GetProperties().AsMap()["memory_size"])
	}
	resource(t, set, "urn:pulumi:victor::with-sst::sst:aws:Bucket$aws:s3/bucket:Bucket::Uploads Bucket")
	scopeOf(t, set, "stage:victor/component:Uploads")
	for _, held := range set.GetResources() {
		if held.GetId() == "urn:pulumi:victor::with-sst::aws:ec2/vpcEndpoint:VpcEndpoint::S3" {
			t.Errorf("the deleted endpoint survived the merge: %v", held)
		}
	}
	resource(t, set, "urn:pulumi:victor::with-sst::aws:ec2/vpcEndpoint:VpcEndpoint::Kms")
}

func TestSomethingThatIsNoneOfTheThreeEnvelopesIsRefused(t *testing.T) {
	for _, raw := range []string{`{"hello":"world"}`, `not json at all`} {
		_, err := pulumi.Parse([]byte(raw), pulumi.Options{Source: pulumi.SourcePulumi, Name: "dev"})
		if err == nil || !strings.Contains(err.Error(), "pulumi preview") {
			t.Errorf("Parse(%q) = %v, want a refusal naming the three envelopes it reads", raw, err)
		}
	}
}

func TestTheStackFallsOutOfTheURNsWhenTheCallerDoesNotNameIt(t *testing.T) {
	set := parse(t, "preview.json", pulumi.Options{Source: pulumi.SourcePulumi})

	if got := set.GetScopes()[0].GetName(); got != "dev" {
		t.Errorf("root scope name = %q, want the stack the urns were written under", got)
	}
}

func TestAListHoldingAnUnknownIsUnknownWholeRatherThanShortByOne(t *testing.T) {
	set, err := pulumi.Merge(fixture(t, "sst_state.json"), fixture(t, "sst_diff.json"), pulumi.Options{Source: pulumi.SourceSST, Name: "victor"})
	if err != nil {
		t.Fatalf("Merge() = %v", err)
	}

	endpoint := resource(t, set, "urn:pulumi:victor::with-sst::aws:ec2/vpcEndpoint:VpcEndpoint::Secrets")
	if len(endpoint.GetUnknown()) != 1 || endpoint.GetUnknown()[0] != "subnet_ids" {
		t.Errorf("unknown = %v, want the list itself named rather than the index of an element", endpoint.GetUnknown())
	}
	if _, present := endpoint.GetProperties().AsMap()["subnet_ids"]; present {
		t.Errorf("properties = %v, want no list a caller could count", endpoint.GetProperties().AsMap())
	}
}
