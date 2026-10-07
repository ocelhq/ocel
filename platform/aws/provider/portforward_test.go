package aws

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

func TestAnAWSTargetForwardsPortsAndForwardsNothingWhenNoBindingAsksWithoutReachingAWS(t *testing.T) {
	cfg := aws.Config{Region: "eu-west-2", Credentials: credentials.NewStaticCredentialsProvider("AKID", "secret", ""), BaseEndpoint: aws.String("http://127.0.0.1:1")}
	forward := NewProvider(Options{Region: "eu-west-2"}, nil, cfg, defaultNamespace).Hooks().ForwardPorts
	if forward == nil {
		t.Fatal("the aws provider sets no ForwardPorts hook, so a build never gets the bindings of a private Aurora or Valkey")
	}

	forwards, err := forward(context.Background(), provider.PortForwardRequest{Tier: environment.TierProduction})

	if err != nil || len(forwards) != 0 {
		t.Errorf("ForwardPorts() of no binding = %v, %v, want nothing forwarded and no call to AWS", forwards, err)
	}
}
