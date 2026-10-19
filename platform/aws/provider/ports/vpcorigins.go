package ports

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
)

type VPCOriginsAPI interface {
	ListVpcOrigins(ctx context.Context, in *cloudfront.ListVpcOriginsInput, opts ...func(*cloudfront.Options)) (*cloudfront.ListVpcOriginsOutput, error)
	GetVpcOrigin(ctx context.Context, in *cloudfront.GetVpcOriginInput, opts ...func(*cloudfront.Options)) (*cloudfront.GetVpcOriginOutput, error)
	DeleteVpcOrigin(ctx context.Context, in *cloudfront.DeleteVpcOriginInput, opts ...func(*cloudfront.Options)) (*cloudfront.DeleteVpcOriginOutput, error)
	ListTagsForResource(ctx context.Context, in *cloudfront.ListTagsForResourceInput, opts ...func(*cloudfront.Options)) (*cloudfront.ListTagsForResourceOutput, error)
}
