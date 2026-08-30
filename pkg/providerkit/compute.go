package providerkit

type Compute string

const (
	ComputeServerless Compute = "serverless"
	ComputeContainer  Compute = "container"
)

func ComputeNames(computes []Compute) []string {
	names := make([]string, 0, len(computes))
	for _, compute := range computes {
		names = append(names, string(compute))
	}
	return names
}
