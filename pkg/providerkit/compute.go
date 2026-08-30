package providerkit

import "slices"

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
