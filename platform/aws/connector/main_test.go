package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/ocelhq/ocel/pkg/connectorkit"
)

type proxied struct {
	requests []events.APIGatewayV2HTTPRequest
}

func (p *proxied) ProxyWithContext(_ context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	p.requests = append(p.requests, req)
	return events.APIGatewayV2HTTPResponse{StatusCode: http.StatusOK}, nil
}

type parameters map[string]string

func (p parameters) GetParameter(_ context.Context, in *ssm.GetParameterInput, _ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	value, present := p[aws.ToString(in.Name)]
	if !present {
		return nil, &ssmtypes.ParameterNotFound{}
	}
	return &ssm.GetParameterOutput{Parameter: &ssmtypes.Parameter{Value: aws.String(value)}}, nil
}

func TestTheKeyReadOutOfTheParameterSignsAsTheSeedTheInstallWrote(t *testing.T) {
	t.Parallel()

	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}
	identity, err := keyed(context.Background(), parameters{"/ocel/connector/key": base64.StdEncoding.EncodeToString(seed) + "\n"}, "/ocel/connector/key")
	if err != nil {
		t.Fatalf("keyed: %v", err)
	}
	want := base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
	if identity.PublicKey() != want {
		t.Errorf("the connector signs as %s, the install registered %s", identity.PublicKey(), want)
	}
	if _, err := keyed(context.Background(), parameters{}, "/ocel/connector/key"); err == nil {
		t.Error("a parameter that is not there yielded an identity")
	}
	if _, err := identityOf("not base64!"); err == nil {
		t.Error("a value that is no key yielded an identity")
	}
}

func TestAScheduledWakeBeatsAndAnythingElseIsServedAsARequest(t *testing.T) {
	t.Parallel()

	var beats atomic.Int64
	console := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		beats.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(console.Close)
	identity, err := connectorkit.IdentityFromSeed(make([]byte, ed25519.SeedSize))
	if err != nil {
		t.Fatal(err)
	}
	spec := connectorkit.Spec{
		Config:   connectorkit.Config{Console: console.URL, ConnectorID: "conn-1", OrganizationID: "org-1"},
		Identity: identity,
	}
	serve := &proxied{}
	handle := invoked(spec, serve)

	if _, err := handle(context.Background(), json.RawMessage(`{"ocel":"heartbeat"}`)); err != nil {
		t.Fatalf("a scheduled wake failed: %v", err)
	}
	if beats.Load() != 1 || len(serve.requests) != 0 {
		t.Errorf("a scheduled wake beat %d times and served %d requests, want one beat and nothing served", beats.Load(), len(serve.requests))
	}

	request, err := json.Marshal(events.APIGatewayV2HTTPRequest{RawPath: "/v1/capabilities"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle(context.Background(), request); err != nil {
		t.Fatalf("a request failed: %v", err)
	}
	if beats.Load() != 1 || len(serve.requests) != 1 || serve.requests[0].RawPath != "/v1/capabilities" {
		t.Errorf("a request beat %d times and served %v, want it served and nothing beaten", beats.Load(), serve.requests)
	}
}
