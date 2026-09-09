package alb

import (
	"context"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Program func(ctx *pulumi.Context) error

type Target struct {
	Class edge.Class
	Slug  string
}

func (t Target) Name() string {
	if t.Slug == "" {
		return FrontStack(t.Class)
	}
	return BindingStack(t.Slug, t.Class)
}

func (t Target) Prefix() string {
	if t.Slug == "" {
		return dashed(string(Kind), "front", string(t.Class))
	}
	return dashed(string(Kind), "project", naming.Sanitize(t.Slug))
}

type Stacks interface {
	Up(ctx context.Context, target Target, program Program, report edge.Reporter) (map[string]string, error)

	Destroy(ctx context.Context, target Target, report edge.Reporter) error

	Outputs(ctx context.Context, target Target) (map[string]string, error)
}

type Routes interface {
	Route(ctx context.Context, urlMap, hostname, backend string) error

	Hold(ctx context.Context, urlMap, hostname string) error

	Unroute(ctx context.Context, urlMap, hostname string) error
}

type Front struct {
	Address        string `json:"address,omitempty"`
	CertificateMap string `json:"certificateMap,omitempty"`
	URLMap         string `json:"urlMap,omitempty"`
	NotFound       string `json:"notFound,omitempty"`
}

func (f Front) standing() bool {
	return f.Address != "" && f.CertificateMap != "" && f.URLMap != "" && f.NotFound != ""
}

const (
	outputAddress        = "address"
	outputCertificateMap = "certificateMap"
	outputURLMap         = "urlMap"
	outputNotFound       = "notFound"
)

func frontOf(outputs map[string]string) Front {
	return Front{
		Address:        outputs[outputAddress],
		CertificateMap: outputs[outputCertificateMap],
		URLMap:         outputs[outputURLMap],
		NotFound:       outputs[outputNotFound],
	}
}

func FrontStack(class edge.Class) string { return dashed(string(Kind), "front", string(class)) }

func BindingStack(slug string, class edge.Class) string {
	return dashed(string(Kind), string(class), naming.Sanitize(slug))
}
