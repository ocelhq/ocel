package deploy

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/functionnetwork"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/network"
)

type fixedPass struct {
	result transform.Result
	asked  []transform.Request
}

func (f *fixedPass) Evaluate(_ context.Context, req transform.Request) ([]transform.Result, error) {
	f.asked = append(f.asked, req)
	results := make([]transform.Result, len(req.Resources))
	for i, resource := range req.Resources {
		if resource.Type == transformTypeNetwork {
			results[i] = f.result
		}
	}
	return results, nil
}

func runNetworkWith(t *testing.T, tier environment.Tier, pass transform.Pass) (*inputRecorder, tierPatches) {
	t.Helper()
	patches, err := evaluateTierPatches(context.Background(), pass, testNamespace, tier)
	if err != nil {
		t.Fatalf("evaluateTierPatches() = %v", err)
	}
	layout, err := network.NewLayout(network.DefaultCIDR("ocel", tier))
	if err != nil {
		t.Fatal(err)
	}
	rec := &inputRecorder{}
	run := func(program func(*pulumi.Context) error) {
		if err := pulumi.RunErr(program, pulumi.WithMocks(network.Slug, "production--infra", rec)); err != nil {
			t.Fatalf("run: %v", err)
		}
	}
	run(network.NewProgram("ocel", tier, "us-east-1", layout, []string{"use1-az1", "use1-az2"}, patches.network.Patches).Run)
	run(functionnetwork.NewProgram("ocel", tier, network.Pending, patches.functionNetwork).Run)
	return rec, patches
}

func TestATransformPatchesTheTiersNetworkAndEveryZoneOfASubnetKindItNames(t *testing.T) {
	t.Parallel()

	pass := &fixedPass{result: transform.Result{Patches: transform.Patches{
		"vpc":           {"enableNetworkAddressUsageMetrics": true},
		"privateSubnet": {"enableResourceNameDnsARecordOnLaunch": true},
		"natGateway":    {"secondaryPrivateIpAddressCount": float64(2)},
	}}}
	patches, err := evaluateTierPatches(context.Background(), pass, testNamespace, environment.TierProduction)
	if err != nil {
		t.Fatalf("evaluateTierPatches() = %v", err)
	}

	if len(pass.asked) != 1 || pass.asked[0].Resources[0].Type != "network" || pass.asked[0].Resources[0].Name != "production" || pass.asked[0].Env != "" {
		t.Fatalf("asked %+v, want one network resource named for the tier and no environment: it belongs to every project of the tier", pass.asked)
	}
	if patches.network.IsEmpty() || patches.functionNetwork.IsEmpty() {
		t.Fatalf("patches = %+v, want both the network and the function network patched", patches)
	}
	every := network.ResourceNames(testNamespace, environment.TierProduction)
	maps.Copy(every, functionnetwork.ResourceNames(testNamespace, environment.TierProduction))
	indexed, err := indexPatches([]transformCandidate{{key: resourceKey{Type: transformTypeNetwork, Name: "production"}, names: every}}, []transform.Result{pass.result})
	if err != nil {
		t.Fatal(err)
	}
	indexed.claimed = map[component.ResourceRef]bool{}
	claimed := func(ref component.ResourceRef, field string) bool {
		merged, ok := indexed.claim(ref, pulumi.Map{})
		_, set := merged[field]
		return ok && set
	}
	for _, ref := range network.ResourceNames(testNamespace, environment.TierProduction)["privateSubnet"] {
		if !claimed(ref, "enableResourceNameDnsARecordOnLaunch") {
			t.Errorf("private subnet %s is not patched, want every zone's", ref.Name)
		}
	}
	for _, ref := range network.ResourceNames(testNamespace, environment.TierProduction)["publicSubnet"] {
		if claimed(ref, "enableResourceNameDnsARecordOnLaunch") {
			t.Errorf("public subnet %s took the private subnets' patch", ref.Name)
		}
	}
	if nats := functionnetwork.ResourceNames(testNamespace, environment.TierProduction)["natGateway"]; len(nats) != 2 || !claimed(nats[0], "secondaryPrivateIpAddressCount") || !claimed(nats[1], "secondaryPrivateIpAddressCount") {
		t.Errorf("NAT gateways %v are not all patched, want both of production's", nats)
	}
	if !claimed(network.ResourceNames(testNamespace, environment.TierProduction)["vpc"][0], "enableNetworkAddressUsageMetrics") {
		t.Error("the VPC is not patched")
	}
}

func TestATransformSetsTheBlockOfTheTiersNetworkWhichOcelCarvesItsSubnetsFrom(t *testing.T) {
	t.Parallel()

	pass := &fixedPass{result: transform.Result{Patches: transform.Patches{"vpc": {"cidrBlock": "172.20.0.0/16"}}}}
	patches, err := evaluateTierPatches(context.Background(), pass, testNamespace, environment.TierProduction)
	if err != nil {
		t.Fatalf("evaluateTierPatches() = %v", err)
	}
	if patches.network.CIDR != "172.20.0.0/16" {
		t.Errorf("the network's block = %q, want the one the transform sets", patches.network.CIDR)
	}
	if !patches.network.IsEmpty() {
		t.Errorf("patches = %+v, want no patch left: ocel places the VPC on the block and carves its subnets from it itself", patches.network)
	}
}

func TestABlockForTheTiersNetworkThatIsNotAStringIsRefusedByName(t *testing.T) {
	t.Parallel()

	pass := &fixedPass{result: transform.Result{Patches: transform.Patches{"vpc": {"cidrBlock": float64(16)}}}}
	_, err := evaluateTierPatches(context.Background(), pass, testNamespace, environment.TierProduction)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "vpc.cidrBlock") {
		t.Fatalf("evaluateTierPatches() = %v, want a refusal naming vpc.cidrBlock", err)
	}
}

func TestABlockOcelCannotCarveTheTiersNetworkFromIsRefusedWhenItIsEvaluated(t *testing.T) {
	t.Parallel()

	for _, block := range []string{"10.0.0.0/8", "100.64.0.0/16", "10.64.0.1/16", "a block"} {
		pass := &fixedPass{result: transform.Result{Patches: transform.Patches{"vpc": {"cidrBlock": block}}}}
		_, err := evaluateTierPatches(context.Background(), pass, testNamespace, environment.TierProduction)
		var refused refusal.Refusal
		if !errors.As(err, &refused) || !strings.Contains(refused.Message, "vpc.cidrBlock") || !strings.Contains(refused.Message, block) {
			t.Errorf("evaluateTierPatches(%s) = %v, want a refusal naming vpc.cidrBlock and the block", block, err)
		}
	}
}

func TestAPatchOfAResourceTheNetworkDoesNotConstructIsRefused(t *testing.T) {
	t.Parallel()

	pass := &fixedPass{result: transform.Result{Patches: transform.Patches{"bastion": {"cpu": "512"}}}}
	_, err := evaluateTierPatches(context.Background(), pass, testNamespace, environment.TierPreview)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "bastion") {
		t.Fatalf("evaluateTierPatches() = %v, want a refusal naming the resource", err)
	}
}

func TestTheNetworksTransformTagsReachEveryResourceItHoldsBesideOcelsOwn(t *testing.T) {
	t.Parallel()

	pass := &fixedPass{result: transform.Result{Tags: map[string]string{"acme:cost-center": "platform"}}}
	rec, patches := runNetworkWith(t, environment.TierPreview, pass)
	if patches.network.IsEmpty() {
		t.Fatal("tags alone left the network unpatched, so a change to them would never reach it")
	}
	for _, token := range []string{"aws:ec2/vpc:Vpc", "aws:ec2/subnet:Subnet", "aws:ec2/natGateway:NatGateway"} {
		for _, name := range rec.registered(token) {
			tags := rec.inputs(token, name)["tags"].ObjectValue()
			if tags["acme:cost-center"].StringValue() != "platform" || tags[resource.PropertyKey("ocel:managed-by")].StringValue() != "ocel" {
				t.Errorf("%s %s is tagged %v, want the transform's tag beside Ocel's", token, name, tags)
			}
		}
	}
}

func TestATransformThatPatchesTheNetworkFromABindingIsRefused(t *testing.T) {
	t.Parallel()

	pass := &fixedPass{result: transform.Result{Patches: transform.Patches{
		"vpc": {"ipv6IpamPoolId": map[string]any{outputPlaceholderKey: map[string]any{"type": "custom", "name": "ipam", "property": "pool"}}},
	}}}
	_, err := evaluateTierPatches(context.Background(), pass, testNamespace, environment.TierProduction)
	var refused refusal.Refusal
	if !errors.As(err, &refused) {
		t.Fatalf("evaluateTierPatches() = %v, want a refusal: the network belongs to every project of the tier and reads none of their records", err)
	}
}

func TestTheNetworksPatchesKeepTheirDigestWhileTheyStayTheSame(t *testing.T) {
	t.Parallel()

	result := transform.Result{Patches: transform.Patches{"vpc": {"enableNetworkAddressUsageMetrics": true}}}
	first, err := evaluateTierPatches(context.Background(), &fixedPass{result: result}, testNamespace, environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	again, err := evaluateTierPatches(context.Background(), &fixedPass{result: result}, testNamespace, environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	if first.network.Digest != again.network.Digest || !first.functionNetwork.IsEmpty() {
		t.Errorf("digests %q then %q, function network %+v: want one digest for one patch, and no function network patch when none names it", first.network.Digest, again.network.Digest, first.functionNetwork)
	}
	changed, err := evaluateTierPatches(context.Background(), &fixedPass{result: transform.Result{Patches: transform.Patches{"vpc": {"enableNetworkAddressUsageMetrics": false}}}}, testNamespace, environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	if changed.network.Digest == first.network.Digest {
		t.Error("a changed patch kept its digest, so the network would never take it")
	}
}
