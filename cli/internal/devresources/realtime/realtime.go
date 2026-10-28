package realtime

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"sync"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/devresources/binding"
	realtimev1 "github.com/ocelhq/ocel/pkg/proto/app/realtime/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/tracing"
	"github.com/ocelhq/ocel/platform/realtime/gateway"
	"github.com/ocelhq/ocel/platform/realtime/gatewayenv"
	"github.com/ocelhq/ocel/platform/realtime/proxy"
)

type Backend struct {
	appOrigins func() []string

	mu      sync.Mutex
	server  *http.Server
	gateway *gateway.Gateway
	service *proxy.Service
	host    string
	keys    map[string]ed25519.PrivateKey
	records map[string]string
}

type readRecord func(key string) string

func (r readRecord) Value(key string) string { return r(key) }

func New(appOrigins func() []string) *Backend {
	return &Backend{appOrigins: appOrigins, keys: map[string]ed25519.PrivateKey{}}
}

func (b *Backend) Resolve(_ context.Context, _ string, resources []declaration.Resource) ([]binding.Resolved, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(resources) > 0 && b.server == nil {
		if err := b.listen(); err != nil {
			return nil, err
		}
	}
	keys := make(map[string]ed25519.PrivateKey, len(resources))
	records := map[string]string{}
	out := make([]binding.Resolved, 0, len(resources))
	for _, resource := range resources {
		key, known := b.keys[resource.Name]
		if !known {
			_, generated, err := ed25519.GenerateKey(nil)
			if err != nil {
				return nil, fmt.Errorf("realtime %q: generate its signing key: %w", resource.Name, err)
			}
			key = generated
		}
		keys[resource.Name] = key
		bound, err := binding.Encode(resource.Type, &bindingsv1.Binding{
			Name: resource.Name,
			Properties: &bindingsv1.Binding_Realtime{Realtime: &bindingsv1.RealtimeProperties{
				Url:        "ws://" + b.host + gatewayenv.SocketPath,
				Host:       b.host,
				SigningKey: key.Seed(),
				VerifyKey:  key.Public().(ed25519.PublicKey),
			}},
		})
		if err != nil {
			return nil, err
		}
		bound.Origin = "realtime gateway @ " + b.host
		maps.Copy(records, bound.Env)
		out = append(out, bound)
	}
	b.keys, b.records = keys, records
	return out, nil
}

func (b *Backend) listen() error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("start the realtime gateway: %w", err)
	}
	b.host = listener.Addr().String()
	b.gateway = gateway.New(gateway.Config{
		Host:           b.host,
		Keys:           b.verifyKey,
		AllowedOrigins: b.appOrigins,
	})
	b.service = proxy.NewService(readRecord(b.readRecord), proxy.NewGatewayTransport(tracing.NewClient(0), "http://"+b.host+gatewayenv.PublishPath))
	server := &http.Server{Handler: b.gateway}
	b.server = server
	go func() { _ = server.Serve(listener) }()
	return nil
}

func (b *Backend) verifyKey(namespace string) (ed25519.PublicKey, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	key, known := b.keys[namespace]
	if !known {
		return nil, false
	}
	return key.Public().(ed25519.PublicKey), true
}

func (b *Backend) readRecord(key string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.records[key]
}

func (b *Backend) Publish(ctx context.Context, req *realtimev1.PublishRequest) (*realtimev1.PublishResponse, error) {
	service, err := b.readService("publish on")
	if err != nil {
		return nil, err
	}
	return service.Publish(ctx, req)
}

func (b *Backend) Mint(ctx context.Context, req *realtimev1.MintRequest) (*realtimev1.MintResponse, error) {
	service, err := b.readService("mint a token for")
	if err != nil {
		return nil, err
	}
	return service.Mint(ctx, req)
}

func (b *Backend) readService(access string) (*proxy.Service, error) {
	b.mu.Lock()
	service := b.service
	b.mu.Unlock()
	if service == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("this app declares no realtime, so there is nothing to %s", access))
	}
	return service, nil
}

func (b *Backend) Close(ctx context.Context, _ bool) error {
	b.mu.Lock()
	b.keys, b.records = map[string]ed25519.PrivateKey{}, nil
	server, realtimeGateway := b.server, b.gateway
	b.server, b.gateway, b.service, b.host = nil, nil, nil, ""
	b.mu.Unlock()
	if server == nil {
		return nil
	}
	if err := server.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("stop the realtime gateway: %w", err)
	}
	if err := realtimeGateway.Close(ctx); err != nil {
		return fmt.Errorf("close the realtime gateway's open sockets: %w", err)
	}
	return nil
}
