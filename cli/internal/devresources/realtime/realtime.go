package realtime

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"

	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/devresources/binding"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/platform/realtime/gateway"
)

type Backend struct {
	appOrigins func() []string

	mu      sync.Mutex
	server  *http.Server
	gateway *gateway.Gateway
	host    string
	keys    map[string]ed25519.PrivateKey
}

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
				Transport:  bindingsv1.RealtimeTransport_REALTIME_TRANSPORT_OCEL_GATEWAY,
				Url:        "ws://" + b.host + gateway.SocketPath,
				Host:       b.host,
				SigningKey: key.Seed(),
				VerifyKey:  key.Public().(ed25519.PublicKey),
			}},
		})
		if err != nil {
			return nil, err
		}
		bound.Origin = "realtime gateway @ " + b.host
		out = append(out, bound)
	}
	b.keys = keys
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

func (b *Backend) Close(ctx context.Context, _ bool) error {
	b.mu.Lock()
	b.keys = map[string]ed25519.PrivateKey{}
	server, realtimeGateway := b.server, b.gateway
	b.server, b.gateway, b.host = nil, nil, ""
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
