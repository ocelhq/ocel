package containers

import (
	"context"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"

	"github.com/ocelhq/ocel/pkg/environment"
)

type fakeVPCOrigin struct {
	summary cftypes.VpcOriginSummary
	tags    map[string]string
}

type fakeVPCOrigins struct {
	origins []fakeVPCOrigin
	deleted []string
}

func (f *fakeVPCOrigins) ListVpcOrigins(_ context.Context, in *cloudfront.ListVpcOriginsInput, _ ...func(*cloudfront.Options)) (*cloudfront.ListVpcOriginsOutput, error) {
	start := 0
	if in.Marker != nil {
		start = slices.IndexFunc(f.origins, func(o fakeVPCOrigin) bool { return aws.ToString(o.summary.Id) == aws.ToString(in.Marker) })
	}
	list := &cftypes.VpcOriginList{Items: []cftypes.VpcOriginSummary{f.origins[start].summary}}
	if start+1 < len(f.origins) {
		list.IsTruncated = aws.Bool(true)
		list.NextMarker = f.origins[start+1].summary.Id
	}
	return &cloudfront.ListVpcOriginsOutput{VpcOriginList: list}, nil
}

func (f *fakeVPCOrigins) find(id string) (fakeVPCOrigin, bool) {
	for _, origin := range f.origins {
		if aws.ToString(origin.summary.Id) == id && !slices.Contains(f.deleted, id) {
			return origin, true
		}
	}
	return fakeVPCOrigin{}, false
}

func (f *fakeVPCOrigins) GetVpcOrigin(_ context.Context, in *cloudfront.GetVpcOriginInput, _ ...func(*cloudfront.Options)) (*cloudfront.GetVpcOriginOutput, error) {
	if _, found := f.find(aws.ToString(in.Id)); !found {
		return nil, &cftypes.EntityNotFound{Message: aws.String("no such vpc origin")}
	}
	return &cloudfront.GetVpcOriginOutput{ETag: aws.String("etag-" + aws.ToString(in.Id))}, nil
}

func (f *fakeVPCOrigins) DeleteVpcOrigin(_ context.Context, in *cloudfront.DeleteVpcOriginInput, _ ...func(*cloudfront.Options)) (*cloudfront.DeleteVpcOriginOutput, error) {
	if aws.ToString(in.IfMatch) != "etag-"+aws.ToString(in.Id) {
		return nil, &cftypes.PreconditionFailed{Message: aws.String("stale etag")}
	}
	f.deleted = append(f.deleted, aws.ToString(in.Id))
	return &cloudfront.DeleteVpcOriginOutput{}, nil
}

func (f *fakeVPCOrigins) ListTagsForResource(_ context.Context, in *cloudfront.ListTagsForResourceInput, _ ...func(*cloudfront.Options)) (*cloudfront.ListTagsForResourceOutput, error) {
	for _, origin := range f.origins {
		if aws.ToString(origin.summary.Arn) != aws.ToString(in.Resource) {
			continue
		}
		var items []cftypes.Tag
		for key, value := range origin.tags {
			items = append(items, cftypes.Tag{Key: aws.String(key), Value: aws.String(value)})
		}
		return &cloudfront.ListTagsForResourceOutput{Tags: &cftypes.Tags{Items: items}}, nil
	}
	return nil, &cftypes.EntityNotFound{Message: aws.String("no such resource")}
}

func vpcOrigin(id, name, status string, tags map[string]string) fakeVPCOrigin {
	return fakeVPCOrigin{
		summary: cftypes.VpcOriginSummary{
			Id:     aws.String(id),
			Arn:    aws.String("arn:aws:cloudfront::123456789012:vpcorigin/" + id),
			Name:   aws.String(name),
			Status: aws.String(status),
		},
		tags: tags,
	}
}

func TestARetriedDeployReclaimsTheVPCOriginAFailedOneLeftBehindUnderTheTiersName(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	ours := stackTags(tier)
	api := &fakeVPCOrigins{origins: []fakeVPCOrigin{
		vpcOrigin("vo_other", "someone-elses-origin", "Failed", nil),
		vpcOrigin("vo_failed", nameFor(tier), "Failed", ours),
	}}

	if err := reclaimFailedVPCOrigins(context.Background(), api, tier, nil); err != nil {
		t.Fatalf("reclaimFailedVPCOrigins() = %v", err)
	}
	if !slices.Equal(api.deleted, []string{"vo_failed"}) {
		t.Errorf("deleted %v, want vo_failed alone: CloudFront refuses a second VPC origin under the name a failed one still holds", api.deleted)
	}
}

func TestARetriedDeployLeavesEveryVPCOriginItCannotProveIsItsOwnFailedOne(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	ours := stackTags(tier)
	untagged := map[string]string{}
	sibling := stackTags(environment.TierPreview)
	api := &fakeVPCOrigins{origins: []fakeVPCOrigin{
		vpcOrigin("vo_deployed", nameFor(tier), "Deployed", ours),
		vpcOrigin("vo_untagged", nameFor(tier), "Failed", untagged),
		vpcOrigin("vo_sibling", nameFor(tier), "Failed", sibling),
		vpcOrigin("vo_renamed", nameFor(tier, "other"), "Failed", ours),
	}}

	if err := reclaimFailedVPCOrigins(context.Background(), api, tier, nil); err != nil {
		t.Fatalf("reclaimFailedVPCOrigins() = %v", err)
	}
	if len(api.deleted) != 0 {
		t.Errorf("deleted %v, want none: a deployed origin serves the tier, and one not tagged as this stack's is not Ocel's to take", api.deleted)
	}
}
