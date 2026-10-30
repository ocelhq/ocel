package alb

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
)

type Stacks interface {
	UpLoadBalancer(ctx context.Context, spec albresources.LoadBalancerSpec, progress progress.Log) (map[string]string, error)

	UpBinding(ctx context.Context, spec albresources.BindingSpec, progress progress.Log) (map[string]string, error)

	UpGate(ctx context.Context, tier environment.Tier, publish func(ctx context.Context, write bool) (albresources.GateSpec, error), progress progress.Log) (map[string]string, error)

	Destroy(ctx context.Context, stack albresources.Stack, progress progress.Log) error

	Outputs(ctx context.Context, stack albresources.Stack) (map[string]string, error)
}

type Routes interface {
	Route(ctx context.Context, urlMap, hostname, backend string, routes *albresources.StaticRoutes) error

	ServeNotFound(ctx context.Context, urlMap, hostname string) error

	CountHostRules(ctx context.Context, urlMap string) (int, error)

	ReadBackendServiceQuota(ctx context.Context) (BackendServiceQuota, bool, error)

	Unroute(ctx context.Context, urlMap, hostname string) error

	InvalidateTags(ctx context.Context, urlMap string, tags []string) error
	InvalidateHostnames(ctx context.Context, urlMap string, hostnames []string) error
}

type BackendServiceQuota struct {
	Usage float64
	Limit float64
}

type Entries interface {
	Entered(ctx context.Context, certificateMap string) ([]string, error)
}

type LoadBalancer struct {
	Address        string `json:"address,omitempty"`
	CertificateMap string `json:"certificateMap,omitempty"`
	URLMap         string `json:"urlMap,omitempty"`
	NotFound       string `json:"notFound,omitempty"`
	AssetsBackend  string `json:"assetsBackend,omitempty"`
}

func (f LoadBalancer) provisioned() bool {
	return f.Address != "" && f.CertificateMap != "" && f.URLMap != "" && f.NotFound != ""
}

func loadBalancerOf(outputs map[string]string) LoadBalancer {
	return LoadBalancer{
		Address:        outputs[albresources.OutputAddress],
		CertificateMap: outputs[albresources.OutputCertificateMap],
		URLMap:         outputs[albresources.OutputURLMap],
		NotFound:       outputs[albresources.OutputNotFound],
		AssetsBackend:  outputs[albresources.OutputAssetsBackend],
	}
}
