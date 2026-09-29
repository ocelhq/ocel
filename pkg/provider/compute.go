package provider

import (
	"slices"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type Compute string

const (
	ComputeServerless Compute = "serverless"
	ComputeContainer  Compute = "container"
)

func Computes() []Compute {
	return []Compute{ComputeServerless, ComputeContainer}
}

func KnownCompute(name string) bool {
	return slices.Contains(Computes(), Compute(name))
}

func ComputeNames(computes []Compute) []string {
	names := make([]string, 0, len(computes))
	for _, compute := range computes {
		names = append(names, string(compute))
	}
	return names
}

func ComputeOf(app *contractv1.ManifestApp) Compute {
	switch app.GetArtifact().(type) {
	case *contractv1.ManifestApp_Serverless:
		return ComputeServerless
	case *contractv1.ManifestApp_Container:
		return ComputeContainer
	}
	return ""
}
