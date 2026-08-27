package pulumi

import (
	"fmt"
	"net"
	"strings"
	"sync"

	provider "github.com/pulumi/pulumi-go-provider"
	rpc "github.com/pulumi/pulumi/sdk/v3/proto/go"
	"google.golang.org/grpc"
)

const debugProvidersEnvVar = "PULUMI_DEBUG_PROVIDERS"

type Plugin struct {
	Package string

	Version string

	Provider provider.Provider
}

type hostedPlugins struct {
	declared []Plugin
	once     sync.Once
	setting  string
	err      error
}

func (h *hostedPlugins) attach() (string, error) {
	if h == nil || len(h.declared) == 0 {
		return "", nil
	}
	h.once.Do(func() {
		attached := make([]string, 0, len(h.declared))
		for _, plugin := range h.declared {
			port, err := host(plugin)
			if err != nil {
				h.err = err
				return
			}
			attached = append(attached, fmt.Sprintf("%s:%d", plugin.Package, port))
		}
		h.setting = strings.Join(attached, ",")
	})
	return h.setting, h.err
}

func host(plugin Plugin) (int, error) {
	server, err := provider.RawServer(plugin.Package, plugin.Version, plugin.Provider)(nil)
	if err != nil {
		return 0, fmt.Errorf("build the %s plugin the engine attaches to: %w", plugin.Package, err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("listen for the engine's calls into the %s plugin: %w", plugin.Package, err)
	}
	serving := grpc.NewServer()
	rpc.RegisterResourceProviderServer(serving, server)
	go serving.Serve(listener)
	return listener.Addr().(*net.TCPAddr).Port, nil
}
