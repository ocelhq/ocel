package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/ocelhq/ocel/pkg/connectorserver"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/proto/console/v1/consolev1connect"
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

type heartbeatCounter struct {
	consolev1connect.UnimplementedConnectorServiceHandler
	beats   *atomic.Int64
	refusal error
}

func (c heartbeatCounter) Heartbeat(context.Context, *consolev1.HeartbeatConnectorRequest) (*consolev1.HeartbeatConnectorResponse, error) {
	c.beats.Add(1)
	if c.refusal != nil {
		return nil, c.refusal
	}
	return &consolev1.HeartbeatConnectorResponse{}, nil
}

func invokedAgainst(t *testing.T, origin string) func(context.Context, json.RawMessage) (any, error) {
	t.Helper()
	identity, err := connectorserver.IdentityFromSeed(make([]byte, ed25519.SeedSize))
	if err != nil {
		t.Fatal(err)
	}
	spec := connectorserver.Spec{
		Config:   connectorserver.Config{Console: origin, ConnectorID: "0199b5c4-0000-7000-8000-00000000cafe", OrganizationID: "org-1"},
		Identity: identity,
	}
	return invoked(spec, &proxied{})
}

func TestAScheduledWakeTheConsoleRefusesSucceedsSoLambdaDoesNotRetryIt(t *testing.T) {
	t.Parallel()

	var beats atomic.Int64
	mux := http.NewServeMux()
	path, handler := consolev1connect.NewConnectorServiceHandler(heartbeatCounter{
		beats:   &beats,
		refusal: connect.NewError(connect.CodeNotFound, errors.New("no such connector")),
	})
	mux.Handle("/api/connect"+path, http.StripPrefix("/api/connect", handler))
	console := httptest.NewServer(mux)
	t.Cleanup(console.Close)

	if _, err := invokedAgainst(t, console.URL)(context.Background(), json.RawMessage(`{"ocel":"heartbeat"}`)); err != nil {
		t.Fatalf("a wake the console refused failed, and Lambda retries a failed wake: %v", err)
	}
	if beats.Load() != 1 {
		t.Errorf("the console saw %d beats, want 1", beats.Load())
	}
}

func TestAScheduledWakeThatDoesNotReachTheConsoleFails(t *testing.T) {
	t.Parallel()

	console := httptest.NewServer(http.NotFoundHandler())
	origin := console.URL
	console.Close()

	if _, err := invokedAgainst(t, origin)(context.Background(), json.RawMessage(`{"ocel":"heartbeat"}`)); err == nil {
		t.Fatal("a wake that never reached the console succeeded")
	}
}

func TestAScheduledWakeBeatsAndAnythingElseIsServedAsARequest(t *testing.T) {
	t.Parallel()

	var beats atomic.Int64
	mux := http.NewServeMux()
	path, handler := consolev1connect.NewConnectorServiceHandler(heartbeatCounter{beats: &beats})
	mux.Handle("/api/connect"+path, http.StripPrefix("/api/connect", handler))
	console := httptest.NewServer(mux)
	t.Cleanup(console.Close)
	identity, err := connectorserver.IdentityFromSeed(make([]byte, ed25519.SeedSize))
	if err != nil {
		t.Fatal(err)
	}
	spec := connectorserver.Spec{
		Config:   connectorserver.Config{Console: console.URL, ConnectorID: "0199b5c4-0000-7000-8000-00000000cafe", OrganizationID: "org-1"},
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

func TestTheConnectorProvesItsIdentityAsItsOwnRole(t *testing.T) {
	cfg := aws.Config{Region: "eu-west-2", Credentials: credentials.NewStaticCredentialsProvider("AKID", "secret", "")}
	prove := variableStore(cfg, nil).ProveIdentity
	if prove == nil {
		t.Fatal("the connector's backend has no ProveIdentity, so an Infisical env source with identity auth is refused through the console")
	}
	if proof, err := prove(context.Background(), "identity-1"); err != nil || proof.SignedRequest == nil || proof.SignedRequest.URL != "https://sts.eu-west-2.amazonaws.com/" {
		t.Fatalf("ProveIdentity() = %+v, %v, want a request signed for the connector's own region", proof, err)
	}
}
