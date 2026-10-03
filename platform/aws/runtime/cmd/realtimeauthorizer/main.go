package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/appsync"

	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
	"github.com/ocelhq/ocel/platform/realtime/token"
)

const (
	operationConnect   = "EVENT_CONNECT"
	operationSubscribe = "EVENT_SUBSCRIBE"

	httpDNSKey = "HTTP"
)

type requestContext struct {
	APIID                string `json:"apiId"`
	Operation            string `json:"operation"`
	ChannelNamespaceName string `json:"channelNamespaceName"`
	Channel              string `json:"channel"`
}

type authorizationRequest struct {
	AuthorizationToken string         `json:"authorizationToken"`
	RequestContext     requestContext `json:"requestContext"`
}

type authorizationResponse struct {
	IsAuthorized bool `json:"isAuthorized"`
	TTLOverride  int  `json:"ttlOverride"`
}

type apiHosts struct {
	read  func(ctx context.Context, apiID string) (string, error)
	mu    sync.Mutex
	found map[string]string
}

func newAPIHosts(read func(ctx context.Context, apiID string) (string, error)) *apiHosts {
	return &apiHosts{read: read, found: map[string]string{}}
}

func (h *apiHosts) find(ctx context.Context, apiID string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if host, found := h.found[apiID]; found {
		return host, nil
	}
	host, err := h.read(ctx, apiID)
	if err != nil {
		return "", err
	}
	h.found[apiID] = host
	return host, nil
}

type authorizer struct {
	keys  map[string]ed25519.PublicKey
	hosts *apiHosts
	now   func() time.Time
}

func (a *authorizer) authorize(ctx context.Context, req authorizationRequest) authorizationResponse {
	err := a.verify(ctx, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel realtime authorizer: %s refused: %s\n", req.RequestContext.Operation, err)
	}
	return authorizationResponse{IsAuthorized: err == nil, TTLOverride: 0}
}

func (a *authorizer) verify(ctx context.Context, req authorizationRequest) error {
	raw := req.AuthorizationToken
	want := token.Expected{}
	switch req.RequestContext.Operation {
	case operationConnect:
		namespace, err := token.ReadUnverifiedNamespace(raw)
		if err != nil {
			return err
		}
		want = token.Expected{Namespace: namespace, Operation: token.Connect, Channel: "/" + namespace}
	case operationSubscribe:
		want = token.Expected{
			Namespace: req.RequestContext.ChannelNamespaceName,
			Operation: token.Subscribe,
			Channel:   normalizeChannel(req.RequestContext.Channel),
		}
	default:
		return fmt.Errorf("operation %q is the app role's alone", req.RequestContext.Operation)
	}
	key, known := a.keys[want.Namespace]
	if !known {
		return &token.Refusal{Reason: token.ReasonNamespace}
	}
	host, err := a.hosts.find(ctx, req.RequestContext.APIID)
	if err != nil {
		return fmt.Errorf("read the host of api %s: %w", req.RequestContext.APIID, err)
	}
	want.Audience = host
	_, err = token.Verify(raw, key, a.now(), want)
	return err
}

func normalizeChannel(channel string) string {
	if len(channel) > 1 {
		channel = strings.TrimSuffix(channel, "/")
	}
	if !strings.HasPrefix(channel, "/") {
		channel = "/" + channel
	}
	return channel
}

func readVerifyKeys(raw string) (map[string]ed25519.PublicKey, error) {
	var encoded map[string]string
	if err := json.Unmarshal([]byte(raw), &encoded); err != nil {
		return nil, fmt.Errorf("%s is no JSON object of namespaces to keys: %w", awsports.RealtimeVerifyKeysEnvVar, err)
	}
	if len(encoded) == 0 {
		return nil, fmt.Errorf("%s names no namespace, so every token would be refused", awsports.RealtimeVerifyKeysEnvVar)
	}
	keys := make(map[string]ed25519.PublicKey, len(encoded))
	for namespace, value := range encoded {
		key, err := base64.StdEncoding.DecodeString(value)
		if err != nil || len(key) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("%s holds no Ed25519 public key for namespace %s", awsports.RealtimeVerifyKeysEnvVar, namespace)
		}
		keys[namespace] = ed25519.PublicKey(key)
	}
	return keys, nil
}

func readAPIHost(client *appsync.Client) func(ctx context.Context, apiID string) (string, error) {
	return func(ctx context.Context, apiID string) (string, error) {
		out, err := client.GetApi(ctx, &appsync.GetApiInput{ApiId: aws.String(apiID)})
		if err != nil {
			return "", err
		}
		if out.Api == nil || out.Api.Dns[httpDNSKey] == "" {
			return "", errors.New("the api has no HTTP domain")
		}
		return out.Api.Dns[httpDNSKey], nil
	}
}

func main() {
	keys, err := readVerifyKeys(os.Getenv(awsports.RealtimeVerifyKeysEnvVar))
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel realtime authorizer: %v\n", err)
		os.Exit(1)
	}
	cfg, err := sdkconfig.Workload(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel realtime authorizer: %v\n", err)
		os.Exit(1)
	}
	a := &authorizer{keys: keys, hosts: newAPIHosts(readAPIHost(appsync.NewFromConfig(cfg))), now: time.Now}
	lambda.Start(a.authorize)
}
