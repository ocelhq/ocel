package component

import (
	"maps"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type Patches struct {
	Digest  string
	Tags    map[string]string
	Install func(*pulumi.Context) error
}

func (p Patches) IsEmpty() bool { return p.Digest == "" }

func (p Patches) Apply(ctx *pulumi.Context) error {
	if p.Install == nil {
		return nil
	}
	return p.Install(ctx)
}

func (p Patches) TagsOver(tags map[string]string) map[string]string {
	merged := maps.Clone(p.Tags)
	if merged == nil {
		merged = map[string]string{}
	}
	maps.Copy(merged, tags)
	return merged
}
