package alb

import (
	"context"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
)

type Program func(ctx *pulumi.Context, project string) error

type Target struct {
	Tier     environment.Tier
	Slug     string
	Shielded bool
}

func (t Target) Name() string {
	switch {
	case t.Slug != "":
		return BindingStack(t.Slug, t.Tier)
	case t.Shielded:
		return ShieldedLoadBalancerStack(t.Tier)
	}
	return LoadBalancerStack(t.Tier)
}

func (t Target) Prefix() string {
	if t.Slug == "" {
		return t.Name()
	}
	return dashed(string(Kind), "project", naming.Sanitize(t.Slug))
}

type Stacks interface {
	Up(ctx context.Context, target Target, program Program, progress progress.Log) (map[string]string, error)

	Destroy(ctx context.Context, target Target, progress progress.Log) error

	Outputs(ctx context.Context, target Target) (map[string]string, error)
}

type Routes interface {
	Route(ctx context.Context, urlMap, hostname, backend string) error

	ServeNotFound(ctx context.Context, urlMap, hostname string) error

	Unroute(ctx context.Context, urlMap, hostname string) error
}

type Entries interface {
	Entered(ctx context.Context, certificateMap string) ([]string, error)
}

type LoadBalancer struct {
	Address        string `json:"address,omitempty"`
	CertificateMap string `json:"certificateMap,omitempty"`
	URLMap         string `json:"urlMap,omitempty"`
	NotFound       string `json:"notFound,omitempty"`
	Shielded       bool   `json:"shielded,omitempty"`

	TrustedFingerprint string `json:"-"`
}

func (f LoadBalancer) provisioned() bool {
	return f.Address != "" && f.CertificateMap != "" && f.URLMap != "" && f.NotFound != ""
}

const (
	outputAddress        = "address"
	outputCertificateMap = "certificateMap"
	outputURLMap         = "urlMap"
	outputNotFound       = "notFound"
	outputTrusted        = "trustedFingerprint"
)

func loadBalancerOf(outputs map[string]string) LoadBalancer {
	return LoadBalancer{
		Address:        outputs[outputAddress],
		CertificateMap: outputs[outputCertificateMap],
		URLMap:         outputs[outputURLMap],
		NotFound:       outputs[outputNotFound],

		TrustedFingerprint: outputs[outputTrusted],
	}
}

func LoadBalancerStack(tier environment.Tier) string {
	return dashed(string(Kind), "front", string(tier))
}

func ShieldedLoadBalancerStack(tier environment.Tier) string {
	return dashed(string(Kind), "front", shieldedNameSegment, string(tier))
}

func BindingStack(slug string, tier environment.Tier) string {
	return dashed(string(Kind), string(tier), naming.Sanitize(slug))
}
