package server

import (
	"context"
	"slices"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	envv1 "github.com/ocelhq/ocel/pkg/proto/env/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
)

type outputCFN struct {
	outputs map[string]string
}

func (c outputCFN) DescribeStacks(context.Context, *cloudformation.DescribeStacksInput, ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error) {
	if c.outputs == nil {
		return &cloudformation.DescribeStacksOutput{}, nil
	}
	out := make([]cfntypes.Output, 0, len(c.outputs))
	for key, value := range c.outputs {
		out = append(out, cfntypes.Output{OutputKey: aws.String(key), OutputValue: aws.String(value)})
	}
	return &cloudformation.DescribeStacksOutput{Stacks: []cfntypes.Stack{{Outputs: out}}}, nil
}

func linksServer(t *testing.T) *VarsServer {
	t.Helper()
	return &VarsServer{stores: testStores(&countingCFN{}, newFakeDynamo(), &fakeKMS{}), config: &sessionConfig{}}
}

func serverOn(cfn *outputCFN) *VarsServer {
	ddb, crypto := newFakeDynamo(), &fakeKMS{}
	return &VarsServer{config: &sessionConfig{}, stores: &stores{openAccount: func(context.Context, string) (account, error) {
		return account{CFN: cfn, Dynamo: ddb, KMS: crypto}, nil
	}}}
}

func ordersLink() *linksv1.Link {
	return &linksv1.Link{
		Name:   "orders",
		Source: "sst",
		Properties: &linksv1.Link_Postgres{Postgres: &linksv1.PostgresProperties{
			Host: "h", Port: 5432, Database: "d", Username: "u", Password: "pw",
		}},
	}
}

func networkProperties(t *testing.T) *linksv1.Link_Custom {
	t.Helper()
	custom, err := structpb.NewStruct(map[string]any{
		"subnetIds":        []any{"subnet-0a1", "subnet-0b2"},
		"securityGroupIds": []any{"sg-0c3"},
	})
	if err != nil {
		t.Fatalf("build the published struct: %v", err)
	}
	return &linksv1.Link_Custom{Custom: custom}
}

func codeOf(t *testing.T, err error) connect.Code {
	t.Helper()
	if err == nil {
		t.Fatal("the call was accepted, want it refused")
	}
	return connect.CodeOf(err)
}

func TestSetLinkRoundTripsThroughTheStore(t *testing.T) {
	s := linksServer(t)
	ctx := context.Background()

	set, err := s.SetLink(ctx, &envv1.SetLinkRequest{
		Slug:  "shop",
		Class: deploymentsv1.Environment_CLASS_PRODUCTION,
		Owner: "sst",
		Link:  ordersLink(),
	})
	if err != nil {
		t.Fatalf("SetLink: %v", err)
	}
	if set.GetVersion() == 0 {
		t.Error("SetLink reported version 0, want the record row's own")
	}

	listed, err := s.ListLinks(ctx, &envv1.ListLinksRequest{Slug: "shop", Class: deploymentsv1.Environment_CLASS_PRODUCTION})
	if err != nil {
		t.Fatalf("ListLinks: %v", err)
	}
	if len(listed.GetLinks()) != 1 {
		t.Fatalf("ListLinks = %+v, want the link just set", listed.GetLinks())
	}
	got := listed.GetLinks()[0]
	if got.GetName() != "orders" || got.GetType() != linksv1.LinkType_LINK_TYPE_POSTGRES || got.GetSource() != "sst" || got.GetOwner() != "sst" {
		t.Errorf("ListLinks = %+v", got)
	}

	removed, err := s.RemoveLink(ctx, &envv1.RemoveLinkRequest{
		Slug: "shop", Class: deploymentsv1.Environment_CLASS_PRODUCTION, Name: "orders",
	})
	if err != nil {
		t.Fatalf("RemoveLink: %v", err)
	}
	if !removed.GetRemoved() {
		t.Error("RemoveLink = false for a link it had just published")
	}

	gone, err := s.RemoveLink(ctx, &envv1.RemoveLinkRequest{
		Slug: "shop", Class: deploymentsv1.Environment_CLASS_PRODUCTION, Name: "orders",
	})
	if err != nil {
		t.Fatalf("RemoveLink: %v", err)
	}
	if gone.GetRemoved() {
		t.Error("RemoveLink = true for a name nothing publishes")
	}
}

func TestLinkHandlersRefuseACoordinateNothingBindsTo(t *testing.T) {
	ctx := context.Background()

	for name, req := range map[string]*envv1.SetLinkRequest{
		"no slug": {Class: deploymentsv1.Environment_CLASS_PRODUCTION, Owner: "sst", Link: ordersLink()},
		"the class-wide marker": {
			Slug: "shop", Class: deploymentsv1.Environment_CLASS_PREVIEW, Environment: "*", Owner: "sst", Link: ordersLink(),
		},
		"an environment outside preview": {
			Slug: "shop", Class: deploymentsv1.Environment_CLASS_PRODUCTION, Environment: "pr-9", Owner: "sst", Link: ordersLink(),
		},
		"a delimiter in the environment": {
			Slug: "shop", Class: deploymentsv1.Environment_CLASS_PREVIEW, Environment: "pr#9", Owner: "sst", Link: ordersLink(),
		},
		"a newline in the environment": {
			Slug: "shop", Class: deploymentsv1.Environment_CLASS_PREVIEW, Environment: "pr-9\ndeclare const x: string", Owner: "sst", Link: ordersLink(),
		},
		"a control character in the slug": {
			Slug: "sh\x00op", Class: deploymentsv1.Environment_CLASS_PRODUCTION, Owner: "sst", Link: ordersLink(),
		},
		"a delimiter in the link name": {
			Slug: "shop", Class: deploymentsv1.Environment_CLASS_PRODUCTION, Owner: "sst",
			Link: &linksv1.Link{Name: "or#ders", Source: "sst", Properties: ordersLink().GetProperties()},
		},
		"no link name": {
			Slug: "shop", Class: deploymentsv1.Environment_CLASS_PRODUCTION, Owner: "sst",
			Link: &linksv1.Link{Source: "sst", Properties: ordersLink().GetProperties()},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := linksServer(t).SetLink(ctx, req)
			if got := codeOf(t, err); got != connect.CodeInvalidArgument {
				t.Errorf("SetLink %s = %v (%v), want CodeInvalidArgument", name, got, err)
			}
		})
	}

	t.Run("a preview environment is legal in the preview class", func(t *testing.T) {
		if _, err := linksServer(t).SetLink(ctx, &envv1.SetLinkRequest{
			Slug: "shop", Class: deploymentsv1.Environment_CLASS_PREVIEW, Environment: "pr-9", Owner: "sst", Link: ordersLink(),
		}); err != nil {
			t.Fatalf("SetLink: %v", err)
		}
	})
}

func TestSetLinkRefusesARecordNoConsumerCouldResolve(t *testing.T) {
	ctx := context.Background()

	for name, tc := range map[string]struct {
		owner string
		link  *linksv1.Link
		says  string
	}{
		"a record with no properties": {owner: "sst", link: &linksv1.Link{Name: "orders", Source: "sst"}},
		"an unsourced record":         {owner: "sst", link: &linksv1.Link{Name: "orders", Properties: ordersLink().GetProperties()}},
		"a grant over a wildcard": {owner: "sst", link: &linksv1.Link{
			Name: "orders", Source: "sst", Properties: ordersLink().GetProperties(),
			Grants: []*linksv1.Grant{{Actions: []string{"rds-db:connect"}, Resources: []string{"*"}}},
		}},
		"an unsourced custom record": {
			owner: "sst",
			link:  &linksv1.Link{Name: "network", Properties: networkProperties(t)},
			says:  "only your own infrastructure publishes a custom link; ocel provisions nothing it cannot type",
		},
		"a custom record carrying grants": {
			owner: "sst",
			link: &linksv1.Link{
				Name: "network", Source: "sst", Properties: networkProperties(t),
				Grants: []*linksv1.Grant{{
					Actions:   []string{"ec2:CreateNetworkInterface"},
					Resources: []string{"arn:aws:ec2:eu-west-1:111122223333:subnet/subnet-0a1"},
				}},
			},
			says: "no consumer attaches a custom link's grants yet; a grant nobody attaches is a permission the record claims and no app holds",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := linksServer(t).SetLink(ctx, &envv1.SetLinkRequest{
				Slug: "shop", Class: deploymentsv1.Environment_CLASS_PRODUCTION, Owner: tc.owner, Link: tc.link,
			})
			if got := codeOf(t, err); got != connect.CodeInvalidArgument {
				t.Errorf("SetLink %s = %v (%v), want CodeInvalidArgument", name, got, err)
			}
			if tc.says != "" && !strings.Contains(err.Error(), tc.says) {
				t.Errorf("SetLink %s = %q, missing %q", name, err, tc.says)
			}
		})
	}
}

func TestSetLinkPublishesASourcedCustomRecord(t *testing.T) {
	s := linksServer(t)
	ctx := context.Background()

	if _, err := s.SetLink(ctx, &envv1.SetLinkRequest{
		Slug: "shop", Class: deploymentsv1.Environment_CLASS_PRODUCTION, Owner: "sst",
		Link: &linksv1.Link{Name: "network", Source: "sst", Properties: networkProperties(t)},
	}); err != nil {
		t.Fatalf("SetLink: %v", err)
	}

	listed, err := s.ListLinks(ctx, &envv1.ListLinksRequest{Slug: "shop", Class: deploymentsv1.Environment_CLASS_PRODUCTION})
	if err != nil {
		t.Fatalf("ListLinks: %v", err)
	}
	if len(listed.GetLinks()) != 1 {
		t.Fatalf("ListLinks = %+v, want the custom link just set", listed.GetLinks())
	}
	if got := listed.GetLinks()[0]; got.GetType() != linksv1.LinkType_LINK_TYPE_CUSTOM || got.GetSource() != "sst" {
		t.Errorf("ListLinks = %+v, want a custom record sourced to sst", got)
	}
}

func TestSetLinkRefusesANameAnotherPublisherHolds(t *testing.T) {
	s := linksServer(t)
	ctx := context.Background()
	req := &envv1.SetLinkRequest{
		Slug: "shop", Class: deploymentsv1.Environment_CLASS_PRODUCTION, Owner: "sst", Link: ordersLink(),
	}
	if _, err := s.SetLink(ctx, req); err != nil {
		t.Fatalf("SetLink: %v", err)
	}

	req.Owner = "terraform"
	_, err := s.SetLink(ctx, req)

	if got := codeOf(t, err); got != connect.CodeFailedPrecondition {
		t.Errorf("SetLink = %v (%v), want CodeFailedPrecondition", got, err)
	}
	for _, owner := range []string{"sst", "terraform"} {
		if !strings.Contains(err.Error(), owner) {
			t.Errorf("SetLink said %q, which never names %s", err, owner)
		}
	}
}

func TestLinkHandlersNameAnAbsentSubstrate(t *testing.T) {
	ctx := context.Background()

	t.Run("no bootstrap stack at all", func(t *testing.T) {
		_, err := serverOn(&outputCFN{}).ListLinks(ctx, &envv1.ListLinksRequest{
			Slug: "shop", Class: deploymentsv1.Environment_CLASS_PRODUCTION,
		})
		if err == nil {
			t.Fatal("ListLinks read an account holding no ocel substrate")
		}
		if !strings.Contains(err.Error(), "ocel bootstrap") {
			t.Errorf("ListLinks said %q, which never names what a user runs to fix it", err)
		}
	})

	t.Run("a removal against no bootstrap stack at all", func(t *testing.T) {
		_, err := serverOn(&outputCFN{}).RemoveLink(ctx, &envv1.RemoveLinkRequest{
			Slug: "shop", Class: deploymentsv1.Environment_CLASS_PRODUCTION, Name: "orders",
		})
		if err == nil {
			t.Fatal("RemoveLink reported an account holding no ocel substrate as a store with nothing to remove")
		}
		if !strings.Contains(err.Error(), "ocel bootstrap") {
			t.Errorf("RemoveLink said %q, which never names what a user runs to fix it", err)
		}
	})

	t.Run("a bootstrap without a variable store", func(t *testing.T) {
		_, err := serverOn(&outputCFN{outputs: map[string]string{
			"BootstrapVersion": bootstrapVersionOutput,
			"VarsKeyArn":       "arn:aws:kms:eu-west-1:123456789012:key/abcd",
		}}).SetLink(ctx, &envv1.SetLinkRequest{
			Slug: "shop", Class: deploymentsv1.Environment_CLASS_PRODUCTION, Owner: "sst", Link: ordersLink(),
		})
		if err == nil {
			t.Fatal("SetLink published into a substrate carrying no variable store")
		}
		if !strings.Contains(err.Error(), "ocel bootstrap") {
			t.Errorf("SetLink said %q, which never names what a user runs to fix it", err)
		}
	})
}

func TestListLinksDescribesEachRecordWithoutItsValues(t *testing.T) {
	s := linksServer(t)
	ctx := context.Background()

	for _, link := range []*linksv1.Link{
		ordersLink(),
		{Name: "network", Source: "sst", Properties: networkProperties(t)},
	} {
		if _, err := s.SetLink(ctx, &envv1.SetLinkRequest{
			Slug: "shop", Class: deploymentsv1.Environment_CLASS_PRODUCTION, Owner: "sst", Link: link,
		}); err != nil {
			t.Fatalf("SetLink %s: %v", link.GetName(), err)
		}
	}

	listed, err := s.ListLinks(ctx, &envv1.ListLinksRequest{Slug: "shop", Class: deploymentsv1.Environment_CLASS_PRODUCTION})
	if err != nil {
		t.Fatalf("ListLinks: %v", err)
	}

	want := map[string][]*envv1.PropertyShape{
		"network": {
			{Name: "securityGroupIds", JsonType: "string", List: true},
			{Name: "subnetIds", JsonType: "string", List: true},
		},
		"orders": {
			{Name: "host", JsonType: "string"},
			{Name: "port", JsonType: "number"},
			{Name: "database", JsonType: "string"},
			{Name: "username", JsonType: "string"},
			{Name: "password", JsonType: "string"},
		},
	}
	if len(listed.GetLinks()) != len(want) {
		t.Fatalf("ListLinks = %+v, want one summary per published record", listed.GetLinks())
	}
	for _, got := range listed.GetLinks() {
		expected, published := want[got.GetName()]
		if !published {
			t.Fatalf("ListLinks named %q, which nothing published", got.GetName())
		}
		if len(got.GetProperties()) != len(expected) {
			t.Fatalf("ListLinks %s properties = %+v, want %+v", got.GetName(), got.GetProperties(), expected)
		}
		for i, shape := range got.GetProperties() {
			if !proto.Equal(shape, expected[i]) {
				t.Errorf("ListLinks %s property %d = %+v, want %+v", got.GetName(), i, shape, expected[i])
			}
		}
	}

	fields := (&envv1.PropertyShape{}).ProtoReflect().Descriptor().Fields()
	described := make([]string, 0, fields.Len())
	for i := range fields.Len() {
		described = append(described, string(fields.Get(i).Name()))
	}
	if !slices.Equal(described, []string{"name", "json_type", "list"}) {
		t.Errorf("PropertyShape carries %v; a shape names a property and says how it reads, and carries nothing else", described)
	}

}
