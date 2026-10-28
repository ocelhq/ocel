package pulumi

import (
	"context"
	"fmt"
	"sync"
	"testing"

	provider "github.com/pulumi/pulumi-go-provider"
	rpc "github.com/pulumi/pulumi/sdk/v3/proto/go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestEnginesAttachedAtOnceEachHandshakeAndConfigureTheirOwnPluginServer(t *testing.T) {
	port, err := host(Plugin{Package: "probe", Version: "1.0.0", Provider: provider.Provider{}})
	if err != nil {
		t.Fatalf("host() = %v", err)
	}

	var engines sync.WaitGroup
	for range 8 {
		engines.Go(func() {
			conn, err := grpc.NewClient(fmt.Sprintf("127.0.0.1:%d", port), grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				t.Errorf("dial the hosted plugin: %v", err)
				return
			}
			defer conn.Close()
			engine := rpc.NewResourceProviderClient(conn)
			ctx := context.Background()
			if _, err := engine.Handshake(ctx, &rpc.ProviderHandshakeRequest{EngineAddress: "127.0.0.1:1"}); err != nil {
				t.Errorf("Handshake() = %v", err)
				return
			}
			if _, err := engine.Configure(ctx, &rpc.ConfigureRequest{SendsOldInputs: true, SendsOldInputsToDelete: true}); err != nil {
				t.Errorf("Configure() = %v", err)
			}
		})
	}
	engines.Wait()
}

func TestAnEngineCancellingWhileItConfiguresTheHostedPluginRacesNothing(t *testing.T) {
	cancellable := provider.Provider{Cancel: func(context.Context) error { return nil }}
	port, err := host(Plugin{Package: "probe", Version: "1.0.0", Provider: cancellable})
	if err != nil {
		t.Fatalf("host() = %v", err)
	}
	conn, err := grpc.NewClient(fmt.Sprintf("127.0.0.1:%d", port), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial the hosted plugin: %v", err)
	}
	defer conn.Close()
	engine := rpc.NewResourceProviderClient(conn)
	ctx := context.Background()
	if _, err := engine.Handshake(ctx, &rpc.ProviderHandshakeRequest{EngineAddress: "127.0.0.1:1"}); err != nil {
		t.Fatalf("Handshake() = %v", err)
	}

	var calls sync.WaitGroup
	calls.Go(func() {
		for range 32 {
			if _, err := engine.Configure(ctx, &rpc.ConfigureRequest{SendsOldInputs: true, SendsOldInputsToDelete: true}); err != nil {
				t.Errorf("Configure() = %v", err)
			}
		}
	})
	for range 32 {
		calls.Go(func() {
			if _, err := engine.Cancel(ctx, &emptypb.Empty{}); err != nil {
				t.Errorf("Cancel() = %v", err)
			}
		})
	}
	calls.Wait()
}
