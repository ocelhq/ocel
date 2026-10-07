package bastion

import (
	"fmt"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

type tag struct {
	key   string
	value string
}

func tagsFor(tier environment.Tier) []tag {
	return []tag{{managedByTagKey, managedByTagValue}, {naming.EnvTierTagKey, string(tier)}}
}

func ecsTagsFor(tier environment.Tier) []ecstypes.Tag {
	var out []ecstypes.Tag
	for _, pair := range tagsFor(tier) {
		out = append(out, ecstypes.Tag{Key: aws.String(pair.key), Value: aws.String(pair.value)})
	}
	return out
}

func iamTagsFor(tier environment.Tier) []iamtypes.Tag {
	var out []iamtypes.Tag
	for _, pair := range tagsFor(tier) {
		out = append(out, iamtypes.Tag{Key: aws.String(pair.key), Value: aws.String(pair.value)})
	}
	return out
}

func ec2TagsFor(tier environment.Tier) []ec2types.Tag {
	var out []ec2types.Tag
	for _, pair := range tagsFor(tier) {
		out = append(out, ec2types.Tag{Key: aws.String(pair.key), Value: aws.String(pair.value)})
	}
	return out
}

func isManagedByOcel[T any](tags []T, keyValue func(T) (*string, *string)) bool {
	return slices.ContainsFunc(tags, func(entry T) bool {
		key, value := keyValue(entry)
		return aws.ToString(key) == managedByTagKey && aws.ToString(value) == managedByTagValue
	})
}

func refuseUnowned(kind, name string) error {
	return fmt.Errorf("%s %s exists and is not tagged %s=%s, so Ocel leaves it alone", kind, name, managedByTagKey, managedByTagValue)
}
