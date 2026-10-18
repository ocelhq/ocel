package component

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const (
	TagComponent      = "ocel:component"
	TagBundle         = "ocel:bundle"
	TagResource       = "ocel:resource"
	MaxDescriptionLen = 256
)

func Tags(kind naming.Kind, bundle string, extra map[string]string) pulumi.StringMap {
	tags := pulumi.StringMap{}
	for key, value := range extra {
		tags[key] = pulumi.String(value)
	}
	tags[TagComponent] = pulumi.String(kind.Component())
	if bundle != "" {
		tags[TagBundle] = pulumi.String(bundle)
	}
	return tags
}

func Describe(c naming.Coordinate, detail string) pulumi.String {
	return CapDescription(c.Description(detail+" - release "+c.Release.String()), MaxDescriptionLen)
}

func CapDescription(described string, limit int) pulumi.String {
	if len(described) > limit {
		described = strings.ToValidUTF8(described[:limit], "")
	}
	return pulumi.String(described)
}
