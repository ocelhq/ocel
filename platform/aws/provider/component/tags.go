package component

import (
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
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

func HasTags(tags []ec2types.Tag, want map[string]string) bool {
	matched := 0
	for _, tag := range tags {
		value, wanted := want[aws.ToString(tag.Key)]
		if !wanted {
			continue
		}
		if aws.ToString(tag.Value) != value {
			return false
		}
		matched++
	}
	return matched == len(want)
}
