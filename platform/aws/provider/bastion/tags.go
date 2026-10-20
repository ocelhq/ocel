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
	"github.com/ocelhq/ocel/platform/aws/provider/tags"
)

func ecsTagsFor(namespace string, tier environment.Tier) []ecstypes.Tag {
	return tags.Sorted(naming.OcelTags(namespace, tier), func(key, value *string) ecstypes.Tag { return ecstypes.Tag{Key: key, Value: value} })
}

func iamTagsFor(namespace string, tier environment.Tier) []iamtypes.Tag {
	return tags.Sorted(naming.OcelTags(namespace, tier), func(key, value *string) iamtypes.Tag { return iamtypes.Tag{Key: key, Value: value} })
}

func ec2TagsFor(namespace string, tier environment.Tier) []ec2types.Tag {
	return tags.Sorted(naming.OcelTags(namespace, tier), func(key, value *string) ec2types.Tag { return ec2types.Tag{Key: key, Value: value} })
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
