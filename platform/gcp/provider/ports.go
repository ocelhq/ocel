package gcp

import (
	"context"
	"io"

	"github.com/ocelhq/ocel/pkg/providerkit"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func notReady(thing string) error {
	return providerkit.Refuse(providerkit.CodeNotReady, "gcp: %s is not implemented", thing)
}

type bootstrapper struct{}

func (bootstrapper) Catalogue() []providerkit.Feature { return nil }

func (bootstrapper) Describe(_ context.Context, class providerkit.Class) (providerkit.Bootstrap, error) {
	return providerkit.Bootstrap{Class: class}, nil
}

func (bootstrapper) Plan(context.Context, providerkit.BootstrapRequest) (providerkit.Plan, error) {
	return providerkit.Plan{}, notReady("bootstrapping a project")
}

func (bootstrapper) Apply(context.Context, providerkit.BootstrapRequest, providerkit.Reporter) error {
	return notReady("bootstrapping a project")
}

func (bootstrapper) PlanRemoval(context.Context, providerkit.Class) (providerkit.Plan, error) {
	return providerkit.Plan{}, notReady("removing a bootstrap")
}

func (bootstrapper) Remove(context.Context, providerkit.Class, providerkit.Reporter) error {
	return notReady("removing a bootstrap")
}

type releaser struct{}

func (releaser) Plan(context.Context, providerkit.StackPlan, providerkit.Reporter) (providerkit.Plan, error) {
	return providerkit.Plan{}, notReady("releasing a stack")
}

func (releaser) Provision(context.Context, providerkit.StackPlan, providerkit.Reporter) (providerkit.StackResult, error) {
	return providerkit.StackResult{}, notReady("releasing a stack")
}

func (releaser) PlanDestroy(context.Context, providerkit.StackRef, providerkit.Reporter) (providerkit.Plan, error) {
	return providerkit.Plan{}, notReady("destroying a stack")
}

func (releaser) Destroy(context.Context, providerkit.StackRef, providerkit.Reporter) error {
	return notReady("destroying a stack")
}

type artifacts struct{}

func (artifacts) Put(context.Context, providerkit.ArtifactRef, io.Reader) error {
	return notReady("the artifact store")
}

func (artifacts) Has(context.Context, providerkit.ArtifactRef) (bool, error) {
	return false, notReady("the artifact store")
}

func (artifacts) Open(context.Context, providerkit.ArtifactRef) (io.ReadCloser, error) {
	return nil, notReady("the artifact store")
}

func (artifacts) RemovePrefix(context.Context, providerkit.Class, string, providerkit.Reporter) error {
	return notReady("the artifact store")
}

type sealer struct{}

func (sealer) Seal(context.Context, providerkit.Coordinate, []byte) ([]byte, error) {
	return nil, notReady("sealing a value")
}

func (sealer) Open(context.Context, providerkit.Coordinate, []byte) ([]byte, error) {
	return nil, notReady("opening a sealed value")
}

type edges struct{}

func (edges) Supported() []edge.Kind { return []edge.Kind{cloudflare.Kind} }

func (edges) Default() edge.Kind { return cloudflare.Kind }

func (edges) Open(kind edge.Kind) (edge.Edge, error) {
	if kind != cloudflare.Kind {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"this provider cannot front deployments with the %q edge; it fronts them with %s", kind, cloudflare.Kind)
	}
	return cloudflare.New(edgeNamespace), nil
}

type dns struct{}

const dnsCloudflare = providerkit.DNSKind(cloudflare.Kind)

func (dns) Supported() []providerkit.DNSKind { return []providerkit.DNSKind{dnsCloudflare} }

func (dns) Default() providerkit.DNSKind { return "" }

func (dns) Open(kind providerkit.DNSKind, zone string) (edge.DNSWriter, error) {
	if kind != dnsCloudflare {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"this provider cannot write DNS records with %q; it writes them with %s", kind, dnsCloudflare)
	}
	writer, err := cloudflare.NewDNS(zone)
	if err != nil {
		return nil, providerkit.Refuse(providerkit.CodeInvalid, "%s", err)
	}
	return writer, nil
}

var (
	_ providerkit.Bootstrapper  = bootstrapper{}
	_ providerkit.Releaser      = releaser{}
	_ providerkit.ArtifactStore = artifacts{}
	_ providerkit.RecordStore   = records{}
	_ providerkit.Sealer        = sealer{}
	_ providerkit.EdgeRegistry  = edges{}
	_ providerkit.DNSRegistry   = dns{}
)
