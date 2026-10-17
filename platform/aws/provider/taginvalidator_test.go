package aws

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/taginvalidator"
)

func TestTheTagInvalidatorIsRefusedAsNotReadyOnABootstrapThatPredatesItsRole(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tier := environment.TierPreview
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)
	cfg := aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("AKID", "secret", ""), BaseEndpoint: aws.String(server.URL)}
	p := NewProvider(Options{Region: "us-east-1"}, nil, cfg, defaultNamespace)
	p.identity.remember(struct{}{}, &sts.GetCallerIdentityOutput{Account: aws.String("111122223333")})
	p.deployed.remember(tier, bootstrap.Reading{Deployed: bootstrap.Deployed{
		Present: true, StateBucket: "state", ArtifactBucket: "artifacts", StateTable: "state-table", VariablesTable: "variables", AssetBucket: "assets",
		StateTableStreamARN: "arn:aws:dynamodb:us-east-1:111122223333:table/state-table/stream/2026-10-16T00:00:00.000",
	}})

	work, err := p.partWorkFor(ctx, tier, taginvalidator.Part)
	if err != nil {
		t.Fatalf("partWorkFor() = %v", err)
	}
	_, err = work.make(ctx, nil, false)

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
