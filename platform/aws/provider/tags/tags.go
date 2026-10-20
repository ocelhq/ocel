package tags

import (
	"maps"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
)

func Sorted[T any](tags map[string]string, tag func(key, value *string) T) []T {
	out := make([]T, 0, len(tags))
	for _, key := range slices.Sorted(maps.Keys(tags)) {
		out = append(out, tag(aws.String(key), aws.String(tags[key])))
	}
	return out
}
