package cloudfront

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
)

type cloudFrontCalls struct {
	createKeyValueStore         func(context.Context, *cloudfront.CreateKeyValueStoreInput, ...func(*cloudfront.Options)) (*cloudfront.CreateKeyValueStoreOutput, error)
	describeKeyValueStore       func(context.Context, *cloudfront.DescribeKeyValueStoreInput, ...func(*cloudfront.Options)) (*cloudfront.DescribeKeyValueStoreOutput, error)
	deleteKeyValueStore         func(context.Context, *cloudfront.DeleteKeyValueStoreInput, ...func(*cloudfront.Options)) (*cloudfront.DeleteKeyValueStoreOutput, error)
	createFunction              func(context.Context, *cloudfront.CreateFunctionInput, ...func(*cloudfront.Options)) (*cloudfront.CreateFunctionOutput, error)
	describeFunction            func(context.Context, *cloudfront.DescribeFunctionInput, ...func(*cloudfront.Options)) (*cloudfront.DescribeFunctionOutput, error)
	updateFunction              func(context.Context, *cloudfront.UpdateFunctionInput, ...func(*cloudfront.Options)) (*cloudfront.UpdateFunctionOutput, error)
	publishFunction             func(context.Context, *cloudfront.PublishFunctionInput, ...func(*cloudfront.Options)) (*cloudfront.PublishFunctionOutput, error)
	deleteFunction              func(context.Context, *cloudfront.DeleteFunctionInput, ...func(*cloudfront.Options)) (*cloudfront.DeleteFunctionOutput, error)
	createCachePolicy           func(context.Context, *cloudfront.CreateCachePolicyInput, ...func(*cloudfront.Options)) (*cloudfront.CreateCachePolicyOutput, error)
	getCachePolicy              func(context.Context, *cloudfront.GetCachePolicyInput, ...func(*cloudfront.Options)) (*cloudfront.GetCachePolicyOutput, error)
	updateCachePolicy           func(context.Context, *cloudfront.UpdateCachePolicyInput, ...func(*cloudfront.Options)) (*cloudfront.UpdateCachePolicyOutput, error)
	listCachePolicies           func(context.Context, *cloudfront.ListCachePoliciesInput, ...func(*cloudfront.Options)) (*cloudfront.ListCachePoliciesOutput, error)
	deleteCachePolicy           func(context.Context, *cloudfront.DeleteCachePolicyInput, ...func(*cloudfront.Options)) (*cloudfront.DeleteCachePolicyOutput, error)
	createResponseHeadersPolicy func(context.Context, *cloudfront.CreateResponseHeadersPolicyInput, ...func(*cloudfront.Options)) (*cloudfront.CreateResponseHeadersPolicyOutput, error)
	getResponseHeadersPolicy    func(context.Context, *cloudfront.GetResponseHeadersPolicyInput, ...func(*cloudfront.Options)) (*cloudfront.GetResponseHeadersPolicyOutput, error)
	updateResponseHeadersPolicy func(context.Context, *cloudfront.UpdateResponseHeadersPolicyInput, ...func(*cloudfront.Options)) (*cloudfront.UpdateResponseHeadersPolicyOutput, error)
	listResponseHeadersPolicies func(context.Context, *cloudfront.ListResponseHeadersPoliciesInput, ...func(*cloudfront.Options)) (*cloudfront.ListResponseHeadersPoliciesOutput, error)
	deleteResponseHeadersPolicy func(context.Context, *cloudfront.DeleteResponseHeadersPolicyInput, ...func(*cloudfront.Options)) (*cloudfront.DeleteResponseHeadersPolicyOutput, error)
	createOriginAccessControl   func(context.Context, *cloudfront.CreateOriginAccessControlInput, ...func(*cloudfront.Options)) (*cloudfront.CreateOriginAccessControlOutput, error)
	getOriginAccessControl      func(context.Context, *cloudfront.GetOriginAccessControlInput, ...func(*cloudfront.Options)) (*cloudfront.GetOriginAccessControlOutput, error)
	updateOriginAccessControl   func(context.Context, *cloudfront.UpdateOriginAccessControlInput, ...func(*cloudfront.Options)) (*cloudfront.UpdateOriginAccessControlOutput, error)
	listOriginAccessControls    func(context.Context, *cloudfront.ListOriginAccessControlsInput, ...func(*cloudfront.Options)) (*cloudfront.ListOriginAccessControlsOutput, error)
	deleteOriginAccessControl   func(context.Context, *cloudfront.DeleteOriginAccessControlInput, ...func(*cloudfront.Options)) (*cloudfront.DeleteOriginAccessControlOutput, error)
}

func newCloudFrontCalls(client *cloudfront.Client) cloudFrontCalls {
	return cloudFrontCalls{
		createKeyValueStore:         client.CreateKeyValueStore,
		describeKeyValueStore:       client.DescribeKeyValueStore,
		deleteKeyValueStore:         client.DeleteKeyValueStore,
		createFunction:              client.CreateFunction,
		describeFunction:            client.DescribeFunction,
		updateFunction:              client.UpdateFunction,
		publishFunction:             client.PublishFunction,
		deleteFunction:              client.DeleteFunction,
		createCachePolicy:           client.CreateCachePolicy,
		getCachePolicy:              client.GetCachePolicy,
		updateCachePolicy:           client.UpdateCachePolicy,
		listCachePolicies:           client.ListCachePolicies,
		deleteCachePolicy:           client.DeleteCachePolicy,
		createResponseHeadersPolicy: client.CreateResponseHeadersPolicy,
		getResponseHeadersPolicy:    client.GetResponseHeadersPolicy,
		updateResponseHeadersPolicy: client.UpdateResponseHeadersPolicy,
		listResponseHeadersPolicies: client.ListResponseHeadersPolicies,
		deleteResponseHeadersPolicy: client.DeleteResponseHeadersPolicy,
		createOriginAccessControl:   client.CreateOriginAccessControl,
		getOriginAccessControl:      client.GetOriginAccessControl,
		updateOriginAccessControl:   client.UpdateOriginAccessControl,
		listOriginAccessControls:    client.ListOriginAccessControls,
		deleteOriginAccessControl:   client.DeleteOriginAccessControl,
	}
}

func (c cloudFrontCalls) CreateKeyValueStore(ctx context.Context, in *cloudfront.CreateKeyValueStoreInput, optFns ...func(*cloudfront.Options)) (*cloudfront.CreateKeyValueStoreOutput, error) {
	return c.createKeyValueStore(ctx, in, optFns...)
}

func (c cloudFrontCalls) DescribeKeyValueStore(ctx context.Context, in *cloudfront.DescribeKeyValueStoreInput, optFns ...func(*cloudfront.Options)) (*cloudfront.DescribeKeyValueStoreOutput, error) {
	return c.describeKeyValueStore(ctx, in, optFns...)
}

func (c cloudFrontCalls) DeleteKeyValueStore(ctx context.Context, in *cloudfront.DeleteKeyValueStoreInput, optFns ...func(*cloudfront.Options)) (*cloudfront.DeleteKeyValueStoreOutput, error) {
	return c.deleteKeyValueStore(ctx, in, optFns...)
}

func (c cloudFrontCalls) CreateFunction(ctx context.Context, in *cloudfront.CreateFunctionInput, optFns ...func(*cloudfront.Options)) (*cloudfront.CreateFunctionOutput, error) {
	return c.createFunction(ctx, in, optFns...)
}

func (c cloudFrontCalls) DescribeFunction(ctx context.Context, in *cloudfront.DescribeFunctionInput, optFns ...func(*cloudfront.Options)) (*cloudfront.DescribeFunctionOutput, error) {
	return c.describeFunction(ctx, in, optFns...)
}

func (c cloudFrontCalls) UpdateFunction(ctx context.Context, in *cloudfront.UpdateFunctionInput, optFns ...func(*cloudfront.Options)) (*cloudfront.UpdateFunctionOutput, error) {
	return c.updateFunction(ctx, in, optFns...)
}

func (c cloudFrontCalls) PublishFunction(ctx context.Context, in *cloudfront.PublishFunctionInput, optFns ...func(*cloudfront.Options)) (*cloudfront.PublishFunctionOutput, error) {
	return c.publishFunction(ctx, in, optFns...)
}

func (c cloudFrontCalls) DeleteFunction(ctx context.Context, in *cloudfront.DeleteFunctionInput, optFns ...func(*cloudfront.Options)) (*cloudfront.DeleteFunctionOutput, error) {
	return c.deleteFunction(ctx, in, optFns...)
}

func (c cloudFrontCalls) CreateCachePolicy(ctx context.Context, in *cloudfront.CreateCachePolicyInput, optFns ...func(*cloudfront.Options)) (*cloudfront.CreateCachePolicyOutput, error) {
	return c.createCachePolicy(ctx, in, optFns...)
}

func (c cloudFrontCalls) GetCachePolicy(ctx context.Context, in *cloudfront.GetCachePolicyInput, optFns ...func(*cloudfront.Options)) (*cloudfront.GetCachePolicyOutput, error) {
	return c.getCachePolicy(ctx, in, optFns...)
}

func (c cloudFrontCalls) UpdateCachePolicy(ctx context.Context, in *cloudfront.UpdateCachePolicyInput, optFns ...func(*cloudfront.Options)) (*cloudfront.UpdateCachePolicyOutput, error) {
	return c.updateCachePolicy(ctx, in, optFns...)
}

func (c cloudFrontCalls) ListCachePolicies(ctx context.Context, in *cloudfront.ListCachePoliciesInput, optFns ...func(*cloudfront.Options)) (*cloudfront.ListCachePoliciesOutput, error) {
	return c.listCachePolicies(ctx, in, optFns...)
}

func (c cloudFrontCalls) DeleteCachePolicy(ctx context.Context, in *cloudfront.DeleteCachePolicyInput, optFns ...func(*cloudfront.Options)) (*cloudfront.DeleteCachePolicyOutput, error) {
	return c.deleteCachePolicy(ctx, in, optFns...)
}

func (c cloudFrontCalls) CreateResponseHeadersPolicy(ctx context.Context, in *cloudfront.CreateResponseHeadersPolicyInput, optFns ...func(*cloudfront.Options)) (*cloudfront.CreateResponseHeadersPolicyOutput, error) {
	return c.createResponseHeadersPolicy(ctx, in, optFns...)
}

func (c cloudFrontCalls) GetResponseHeadersPolicy(ctx context.Context, in *cloudfront.GetResponseHeadersPolicyInput, optFns ...func(*cloudfront.Options)) (*cloudfront.GetResponseHeadersPolicyOutput, error) {
	return c.getResponseHeadersPolicy(ctx, in, optFns...)
}

func (c cloudFrontCalls) UpdateResponseHeadersPolicy(ctx context.Context, in *cloudfront.UpdateResponseHeadersPolicyInput, optFns ...func(*cloudfront.Options)) (*cloudfront.UpdateResponseHeadersPolicyOutput, error) {
	return c.updateResponseHeadersPolicy(ctx, in, optFns...)
}

func (c cloudFrontCalls) ListResponseHeadersPolicies(ctx context.Context, in *cloudfront.ListResponseHeadersPoliciesInput, optFns ...func(*cloudfront.Options)) (*cloudfront.ListResponseHeadersPoliciesOutput, error) {
	return c.listResponseHeadersPolicies(ctx, in, optFns...)
}

func (c cloudFrontCalls) DeleteResponseHeadersPolicy(ctx context.Context, in *cloudfront.DeleteResponseHeadersPolicyInput, optFns ...func(*cloudfront.Options)) (*cloudfront.DeleteResponseHeadersPolicyOutput, error) {
	return c.deleteResponseHeadersPolicy(ctx, in, optFns...)
}

func (c cloudFrontCalls) CreateOriginAccessControl(ctx context.Context, in *cloudfront.CreateOriginAccessControlInput, optFns ...func(*cloudfront.Options)) (*cloudfront.CreateOriginAccessControlOutput, error) {
	return c.createOriginAccessControl(ctx, in, optFns...)
}

func (c cloudFrontCalls) GetOriginAccessControl(ctx context.Context, in *cloudfront.GetOriginAccessControlInput, optFns ...func(*cloudfront.Options)) (*cloudfront.GetOriginAccessControlOutput, error) {
	return c.getOriginAccessControl(ctx, in, optFns...)
}

func (c cloudFrontCalls) UpdateOriginAccessControl(ctx context.Context, in *cloudfront.UpdateOriginAccessControlInput, optFns ...func(*cloudfront.Options)) (*cloudfront.UpdateOriginAccessControlOutput, error) {
	return c.updateOriginAccessControl(ctx, in, optFns...)
}

func (c cloudFrontCalls) ListOriginAccessControls(ctx context.Context, in *cloudfront.ListOriginAccessControlsInput, optFns ...func(*cloudfront.Options)) (*cloudfront.ListOriginAccessControlsOutput, error) {
	return c.listOriginAccessControls(ctx, in, optFns...)
}

func (c cloudFrontCalls) DeleteOriginAccessControl(ctx context.Context, in *cloudfront.DeleteOriginAccessControlInput, optFns ...func(*cloudfront.Options)) (*cloudfront.DeleteOriginAccessControlOutput, error) {
	return c.deleteOriginAccessControl(ctx, in, optFns...)
}
