package provider

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type DeploySpec struct {
	Slug    string
	Tier    environment.Tier
	Env     string
	Label   string
	Pointer string

	Infra naming.StackName
	Apps  []AppEntry

	PromotionID string
	Tag         string
	Releases    map[string]string
	Phase       string
}

type AppEntry struct {
	App      string
	Stack    naming.StackName
	Release  Release
	Manifest *contractv1.ManifestApp

	Image           string
	HealthCheckPath string
	Arch            string
	Instances       Instances

	Workers []WorkerSpec
}

type Instances struct {
	Min int
	Max int
}

func (e AppEntry) Compute() Compute { return ComputeOf(e.Manifest) }
