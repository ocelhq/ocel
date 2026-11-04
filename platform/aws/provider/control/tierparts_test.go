package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/tier"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/containers"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/functionnetwork"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/imageoptimizer"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/network"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/sharedparts"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/taginvalidator"
)

func TestTheTagInvalidatorIsRefusedAsNotReadyOnABootstrapThatPredatesItsRole(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	preview := environment.TierPreview
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)
	cfg := aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("AKID", "secret", ""), BaseEndpoint: aws.String(server.URL)}
	p := NewProvider(Options{Region: "us-east-1"}, cfg, defaultNamespace)
	p.identity.remember(struct{}{}, &sts.GetCallerIdentityOutput{Account: aws.String("111122223333")})
	p.deployed.remember(preview, account.Reading{Deployed: account.Deployed{
		Present: true, StateTable: "state-table", VariablesTable: "variables",
		StateTableStreamARN: "arn:aws:dynamodb:us-east-1:111122223333:table/state-table/stream/2026-10-16T00:00:00.000",
		Buckets:             naming.Buckets{naming.StorageState: "state", naming.StorageArtifacts: "artifacts", naming.StorageEdge: "edge", naming.StorageCache: "cache"},
	}})

	spec, err := p.TagInvalidatorSpec(ctx, preview)
	if err != nil {
		t.Fatalf("tagInvalidatorSpec() = %v", err)
	}
	_, err = taginvalidator.Work(taginvalidator.NewClients(p.Config), spec).Make(ctx, nil, false)

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("make() = %v, want a not-ready refusal: the preview bootstrap holds no tag invalidator role to run the function as", err)
	}
	if !strings.Contains(refused.Message, "ocel bootstrap preview") {
		t.Errorf("make() = %q, want it to name `ocel bootstrap preview`, which makes the role", refused.Message)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("AWS was called %d times, want none: a function with no role to run as cannot be created", n)
	}
}

func TestTheControlAssemblyDescribesEveryTierPartAsTheFullProviderDoes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	production := environment.TierProduction
	store := fake.NewKeyValues()
	for _, name := range []string{sharedparts.NetworkPart, sharedparts.FunctionNetworkPart, sharedparts.ContainersPart, imageoptimizer.Part, taginvalidator.Part} {
		if err := stackrecords.MarkBootstrapPartReady(ctx, store, production, name, stackrecords.BootstrapPart{
			WrittenBy: "1.0.0",
			Resources: []provider.PhysicalResource{{Kind: "AWS::EC2::VPC", ID: "vpc-" + name}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := stackrecords.Write(ctx, store, production, awsports.ContainersSlug, naming.InfraStack(string(production)), stackrecords.Stack{Kind: provider.StackInfra}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(ctx, records.Entry{Key: network.Shared.ConsumerAt(containers.StackRef(production)), Value: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	p := NewProvider(Options{Region: "us-east-1"}, aws.Config{Region: "us-east-1"}, defaultNamespace)
	p.identity.remember(struct{}{}, &sts.GetCallerIdentityOutput{Account: aws.String("111122223333")})
	p.deployed.remember(production, account.Reading{Deployed: account.Deployed{
		Present: true, StateTable: "state-table", VariablesTable: "variables",
		Buckets: naming.Buckets{naming.StorageState: "state", naming.StorageArtifacts: "artifacts", naming.StorageEdge: "edge", naming.StorageCache: "cache"},
	}})
	records := tier.Records{KeyValues: store, WrittenBy: "1.0.0", Region: "us-east-1", Reads: &tier.Reads{}}
	namespace := string(defaultNamespace)
	full := p.TierParts(records, EngineParts{
		Bastion: bastion.NewPart(bastion.NewClients(p.Config), namespace, "us-east-1", func(context.Context, environment.Tier, progress.Log) error { return nil }),
		SharedStacks: tier.Parts{
			containers.NewPart(&containers.Stacks{}, store, namespace, "us-east-1"),
			functionnetwork.NewPart(&functionnetwork.Stacks{}, store, "us-east-1"),
			network.NewPart(&network.Stacks{}, store, "us-east-1"),
		},
		ImageOptimizer: imageoptimizer.NewPart(records, imageoptimizer.Clients{}, namespace, p.ImageOptimizerSpec),
		TagInvalidator: taginvalidator.NewPart(records, taginvalidator.Clients{}, namespace, p.TagInvalidatorSpec),
	})
	described := p.TierParts(records, p.describedEngineParts(records))

	if got, want := described.Offered(), full.Offered(); !reflect.DeepEqual(got, want) {
		t.Errorf("the control assembly offers %v, want %v, what the full provider offers", got, want)
	}
	for _, part := range full {
		if part.Describe == nil {
			continue
		}
		served, found := described.Named(part.Name)
		if !found || served.Describe == nil {
			t.Errorf("the control assembly describes no %s part", part.Name)
			continue
		}
		want, wantPresent, wantErr := part.Describe(ctx, production)
		got, gotPresent, gotErr := served.Describe(ctx, production)
		if gotPresent != wantPresent || !reflect.DeepEqual(got, want) || (gotErr == nil) != (wantErr == nil) {
			t.Errorf("the control assembly describes %s as %+v, %v, %v, want %+v, %v, %v", part.Name, got, gotPresent, gotErr, want, wantPresent, wantErr)
		}
	}
}
