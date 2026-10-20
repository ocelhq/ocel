package bastion

import (
	"fmt"
	"iter"
	"maps"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

func eachOcelTag(namespace string, tier environment.Tier) iter.Seq2[string, string] {
	tags := naming.OcelTags(namespace, tier)
	return func(yield func(string, string) bool) {
		for _, key := range slices.Sorted(maps.Keys(tags)) {
			if !yield(key, tags[key]) {
				return
			}
		}
	}
}

func ecsTagsFor(namespace string, tier environment.Tier) []ecstypes.Tag {
	var out []ecstypes.Tag
	for key, value := range eachOcelTag(namespace, tier) {
		out = append(out, ecstypes.Tag{Key: aws.String(key), Value: aws.String(value)})
	}
	return out
}

func iamTagsFor(namespace string, tier environment.Tier) []iamtypes.Tag {
	var out []iamtypes.Tag
	for key, value := range eachOcelTag(namespace, tier) {
		out = append(out, iamtypes.Tag{Key: aws.String(key), Value: aws.String(value)})
	}
	return out
}

func ec2TagsFor(namespace string, tier environment.Tier) []ec2types.Tag {
	var out []ec2types.Tag
	for key, value := range eachOcelTag(namespace, tier) {
		out = append(out, ec2types.Tag{Key: aws.String(key), Value: aws.String(value)})
	}
	return out
}

func isManagedByOcel[T any](namespace string, tags []T, keyValue func(T) (*string, *string)) bool {
	return slices.ContainsFunc(tags, func(entry T) bool {
		key, value := keyValue(entry)
		return aws.ToString(key) == naming.ManagedByTagKey && aws.ToString(value) == namespace
	})
}

func refuseUnowned(namespace, kind, name string) error {
	return fmt.Errorf("%s %s exists and is not tagged %s=%s, so Ocel leaves it alone", kind, name, naming.ManagedByTagKey, namespace)
}
