package names

import "github.com/ocelhq/ocel/pkg/environment"

const (
	logViewWord   = "logs"
	DefaultBucket = "_Default"
)

func (n Names) LogView(tier environment.Tier) string { return n.ocelName(tier, logViewWord) }

func (n Names) LogBucketPath() string {
	return "projects/" + n.project + "/locations/global/buckets/" + DefaultBucket
}

func (n Names) LogViewPath(tier environment.Tier) string {
	return n.LogBucketPath() + "/views/" + n.LogView(tier)
}
