package server

import (
	"context"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
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

func linksServer(t *testing.T) *Server {
	t.Helper()
	return &Server{stores: testStores(&countingCFN{}, newFakeDynamo(), &fakeKMS{})}
}

func serverOn(cfn *outputCFN) *Server {
	ddb, crypto := newFakeDynamo(), &fakeKMS{}
	return &Server{stores: stores{openAccount: func(context.Context, string) (account, error) {
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

	set, err := s.SetLink(ctx, &deploymentsv1.SetLinkRequest{
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

	listed, err := s.ListLinks(ctx, &deploymentsv1.ListLinksRequest{Slug: "shop", Class: deploymentsv1.Environment_CLASS_PRODUCTION})
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

	removed, err := s.RemoveLink(ctx, &deploymentsv1.RemoveLinkRequest{
		Slug: "shop", Class: deploymentsv1.Environment_CLASS_PRODUCTION, Name: "orders",
	})
	if err != nil {
		t.Fatalf("RemoveLink: %v", err)
	}
	if !removed.GetRemoved() {
		t.Error("RemoveLink = false for a link it had just published")
	}

	gone, err := s.RemoveLink(ctx, &deploymentsv1.RemoveLinkRequest{
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

	for name, req := range map[string]*deploymentsv1.SetLinkRequest{
		"no slug": {Class: deploymentsv1.Environment_CLASS_PRODUCTION, Owner: "sst", Link: ordersLink()},
		"an unknown class": {
			Slug: "shop", Class: deploymentsv1.Environment_CLASS_DEVELOPMENT, Owner: "sst", Link: ordersLink(),
		},
		"the class-wide marker": {
			Slug: "shop", Class: deploymentsv1.Environment_CLASS_PREVIEW, Environment: "*", Owner: "sst", Link: ordersLink(),
		},
		"an environment outside preview": {
			Slug: "shop", Class: deploymentsv1.Environment_CLASS_PRODUCTION, Environment: "pr-9", Owner: "sst", Link: ordersLink(),
		},
		"a delimiter in the environment": {
			Slug: "shop", Class: deploymentsv1.Environment_CLASS_PREVIEW, Environment: "pr#9", Owner: "sst", Link: ordersLink(),
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
		if _, err := linksServer(t).SetLink(ctx, &deploymentsv1.SetLinkRequest{
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
	}{
		"a record with no properties": {"sst", &linksv1.Link{Name: "orders", Source: "sst"}},
		"an unsourced record":         {"sst", &linksv1.Link{Name: "orders", Properties: ordersLink().GetProperties()}},
		"a grant over a wildcard": {"sst", &linksv1.Link{
			Name: "orders", Source: "sst", Properties: ordersLink().GetProperties(),
			Grants: []*linksv1.Grant{{Actions: []string{"rds-db:connect"}, Resources: []string{"*"}}},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := linksServer(t).SetLink(ctx, &deploymentsv1.SetLinkRequest{
				Slug: "shop", Class: deploymentsv1.Environment_CLASS_PRODUCTION, Owner: tc.owner, Link: tc.link,
			})
			if got := codeOf(t, err); got != connect.CodeInvalidArgument {
				t.Errorf("SetLink %s = %v (%v), want CodeInvalidArgument", name, got, err)
			}
		})
	}
}

func TestSetLinkRefusesANameAnotherPublisherHolds(t *testing.T) {
	s := linksServer(t)
	ctx := context.Background()
	req := &deploymentsv1.SetLinkRequest{
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
		_, err := serverOn(&outputCFN{}).ListLinks(ctx, &deploymentsv1.ListLinksRequest{
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
		_, err := serverOn(&outputCFN{}).RemoveLink(ctx, &deploymentsv1.RemoveLinkRequest{
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
		}}).SetLink(ctx, &deploymentsv1.SetLinkRequest{
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
