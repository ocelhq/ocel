package notfoundapi

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/apigateway"
)

type APIGatewayCalls struct {
	createRestApi    func(context.Context, *apigateway.CreateRestApiInput, ...func(*apigateway.Options)) (*apigateway.CreateRestApiOutput, error)
	getRestApi       func(context.Context, *apigateway.GetRestApiInput, ...func(*apigateway.Options)) (*apigateway.GetRestApiOutput, error)
	putRestApi       func(context.Context, *apigateway.PutRestApiInput, ...func(*apigateway.Options)) (*apigateway.PutRestApiOutput, error)
	createDeployment func(context.Context, *apigateway.CreateDeploymentInput, ...func(*apigateway.Options)) (*apigateway.CreateDeploymentOutput, error)
	deleteRestApi    func(context.Context, *apigateway.DeleteRestApiInput, ...func(*apigateway.Options)) (*apigateway.DeleteRestApiOutput, error)
}

func NewAPIGatewayCalls(client *apigateway.Client) APIGatewayCalls {
	return APIGatewayCalls{
		createRestApi:    client.CreateRestApi,
		getRestApi:       client.GetRestApi,
		putRestApi:       client.PutRestApi,
		createDeployment: client.CreateDeployment,
		deleteRestApi:    client.DeleteRestApi,
	}
}

func (c APIGatewayCalls) CreateRestApi(ctx context.Context, in *apigateway.CreateRestApiInput, optFns ...func(*apigateway.Options)) (*apigateway.CreateRestApiOutput, error) {
	return c.createRestApi(ctx, in, optFns...)
}

func (c APIGatewayCalls) GetRestApi(ctx context.Context, in *apigateway.GetRestApiInput, optFns ...func(*apigateway.Options)) (*apigateway.GetRestApiOutput, error) {
	return c.getRestApi(ctx, in, optFns...)
}

func (c APIGatewayCalls) PutRestApi(ctx context.Context, in *apigateway.PutRestApiInput, optFns ...func(*apigateway.Options)) (*apigateway.PutRestApiOutput, error) {
	return c.putRestApi(ctx, in, optFns...)
}

func (c APIGatewayCalls) CreateDeployment(ctx context.Context, in *apigateway.CreateDeploymentInput, optFns ...func(*apigateway.Options)) (*apigateway.CreateDeploymentOutput, error) {
	return c.createDeployment(ctx, in, optFns...)
}

func (c APIGatewayCalls) DeleteRestApi(ctx context.Context, in *apigateway.DeleteRestApiInput, optFns ...func(*apigateway.Options)) (*apigateway.DeleteRestApiOutput, error) {
	return c.deleteRestApi(ctx, in, optFns...)
}
