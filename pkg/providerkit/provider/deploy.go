package provider

import (
	"github.com/ocelhq/ocel/pkg/naming"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type DeploySpec struct {
	Slug    string
	Class   edge.Class
	Env     string
	Label   string
	Pointer string

	Infra naming.StackName
	Apps  []AppEntry

	PromotionID string
	Tag         string
	Builds      map[string]string
	Phase       string
}

type AppEntry struct {
	App      string
	Stack    naming.StackName
	Build    Build
	Manifest *contractv1.ManifestApp

	Image           string
	HealthCheckPath string
	Arch            string
}

func (e AppEntry) Compute() Compute { return Compute(e.Manifest.GetCompute()) }
