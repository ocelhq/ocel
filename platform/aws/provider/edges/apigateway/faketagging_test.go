package apigateway

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	rgttypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
)

type fakeTagging struct {
	g *fakeGateway
}

func (f *fakeTagging) GetResources(_ context.Context, in *resourcegroupstaggingapi.GetResourcesInput, _ ...func(*resourcegroupstaggingapi.Options)) (*resourcegroupstaggingapi.GetResourcesOutput, error) {
	f.g.mu.Lock()
	defer f.g.mu.Unlock()
	f.g.record("GetTaggedResources")
	if !slices.Equal(in.ResourceTypeFilters, []string{"apigateway:restapis"}) {
		return nil, fmt.Errorf("fake tagging answers only for REST APIs, not %v", in.ResourceTypeFilters)
	}
	var items []rgttypes.ResourceTagMapping
	for _, id := range slices.Sorted(maps.Keys(f.g.apis)) {
		api := f.g.apis[id]
		if !slices.ContainsFunc(in.TagFilters, func(filter rgttypes.TagFilter) bool {
			value, ok := api.tags[aws.ToString(filter.Key)]
			return !ok || (len(filter.Values) > 0 && !slices.Contains(filter.Values, value))
		}) {
			tags := make([]rgttypes.Tag, 0, len(api.tags))
			for _, key := range slices.Sorted(maps.Keys(api.tags)) {
				tags = append(tags, rgttypes.Tag{Key: aws.String(key), Value: aws.String(api.tags[key])})
			}
			items = append(items, rgttypes.ResourceTagMapping{
				ResourceARN: aws.String("arn:aws:apigateway:" + fakeRegion + "::/restapis/" + id),
				Tags:        tags,
			})
		}
	}
	items, token, err := page(items, in.PaginationToken, f.g.pageSize)
	if err != nil {
		return nil, err
	}
	return &resourcegroupstaggingapi.GetResourcesOutput{ResourceTagMappingList: items, PaginationToken: token}, nil
}
