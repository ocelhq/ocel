package apigateway

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
)

type apiGatewayCalls struct {
	getRestApi                func(context.Context, *apigateway.GetRestApiInput, ...func(*apigateway.Options)) (*apigateway.GetRestApiOutput, error)
	createRestApi             func(context.Context, *apigateway.CreateRestApiInput, ...func(*apigateway.Options)) (*apigateway.CreateRestApiOutput, error)
	tagResource               func(context.Context, *apigateway.TagResourceInput, ...func(*apigateway.Options)) (*apigateway.TagResourceOutput, error)
	deleteRestApi             func(context.Context, *apigateway.DeleteRestApiInput, ...func(*apigateway.Options)) (*apigateway.DeleteRestApiOutput, error)
	getResources              func(context.Context, *apigateway.GetResourcesInput, ...func(*apigateway.Options)) (*apigateway.GetResourcesOutput, error)
	createResource            func(context.Context, *apigateway.CreateResourceInput, ...func(*apigateway.Options)) (*apigateway.CreateResourceOutput, error)
	deleteResource            func(context.Context, *apigateway.DeleteResourceInput, ...func(*apigateway.Options)) (*apigateway.DeleteResourceOutput, error)
	getMethod                 func(context.Context, *apigateway.GetMethodInput, ...func(*apigateway.Options)) (*apigateway.GetMethodOutput, error)
	putMethod                 func(context.Context, *apigateway.PutMethodInput, ...func(*apigateway.Options)) (*apigateway.PutMethodOutput, error)
	deleteMethod              func(context.Context, *apigateway.DeleteMethodInput, ...func(*apigateway.Options)) (*apigateway.DeleteMethodOutput, error)
	putIntegration            func(context.Context, *apigateway.PutIntegrationInput, ...func(*apigateway.Options)) (*apigateway.PutIntegrationOutput, error)
	getMethodResponse         func(context.Context, *apigateway.GetMethodResponseInput, ...func(*apigateway.Options)) (*apigateway.GetMethodResponseOutput, error)
	putMethodResponse         func(context.Context, *apigateway.PutMethodResponseInput, ...func(*apigateway.Options)) (*apigateway.PutMethodResponseOutput, error)
	deleteMethodResponse      func(context.Context, *apigateway.DeleteMethodResponseInput, ...func(*apigateway.Options)) (*apigateway.DeleteMethodResponseOutput, error)
	getIntegrationResponse    func(context.Context, *apigateway.GetIntegrationResponseInput, ...func(*apigateway.Options)) (*apigateway.GetIntegrationResponseOutput, error)
	putIntegrationResponse    func(context.Context, *apigateway.PutIntegrationResponseInput, ...func(*apigateway.Options)) (*apigateway.PutIntegrationResponseOutput, error)
	deleteIntegrationResponse func(context.Context, *apigateway.DeleteIntegrationResponseInput, ...func(*apigateway.Options)) (*apigateway.DeleteIntegrationResponseOutput, error)
	getStage                  func(context.Context, *apigateway.GetStageInput, ...func(*apigateway.Options)) (*apigateway.GetStageOutput, error)
	createDeployment          func(context.Context, *apigateway.CreateDeploymentInput, ...func(*apigateway.Options)) (*apigateway.CreateDeploymentOutput, error)
	updateStage               func(context.Context, *apigateway.UpdateStageInput, ...func(*apigateway.Options)) (*apigateway.UpdateStageOutput, error)
	getDomainName             func(context.Context, *apigateway.GetDomainNameInput, ...func(*apigateway.Options)) (*apigateway.GetDomainNameOutput, error)
	createDomainName          func(context.Context, *apigateway.CreateDomainNameInput, ...func(*apigateway.Options)) (*apigateway.CreateDomainNameOutput, error)
	updateDomainName          func(context.Context, *apigateway.UpdateDomainNameInput, ...func(*apigateway.Options)) (*apigateway.UpdateDomainNameOutput, error)
	deleteDomainName          func(context.Context, *apigateway.DeleteDomainNameInput, ...func(*apigateway.Options)) (*apigateway.DeleteDomainNameOutput, error)
	getBasePathMappings       func(context.Context, *apigateway.GetBasePathMappingsInput, ...func(*apigateway.Options)) (*apigateway.GetBasePathMappingsOutput, error)
	createBasePathMapping     func(context.Context, *apigateway.CreateBasePathMappingInput, ...func(*apigateway.Options)) (*apigateway.CreateBasePathMappingOutput, error)
	deleteBasePathMapping     func(context.Context, *apigateway.DeleteBasePathMappingInput, ...func(*apigateway.Options)) (*apigateway.DeleteBasePathMappingOutput, error)
}

func newAPIGatewayCalls(client *apigateway.Client) apiGatewayCalls {
	return apiGatewayCalls{
		getRestApi:                client.GetRestApi,
		createRestApi:             client.CreateRestApi,
		tagResource:               client.TagResource,
		deleteRestApi:             client.DeleteRestApi,
		getResources:              client.GetResources,
		createResource:            client.CreateResource,
		deleteResource:            client.DeleteResource,
		getMethod:                 client.GetMethod,
		putMethod:                 client.PutMethod,
		deleteMethod:              client.DeleteMethod,
		putIntegration:            client.PutIntegration,
		getMethodResponse:         client.GetMethodResponse,
		putMethodResponse:         client.PutMethodResponse,
		deleteMethodResponse:      client.DeleteMethodResponse,
		getIntegrationResponse:    client.GetIntegrationResponse,
		putIntegrationResponse:    client.PutIntegrationResponse,
		deleteIntegrationResponse: client.DeleteIntegrationResponse,
		getStage:                  client.GetStage,
		createDeployment:          client.CreateDeployment,
		updateStage:               client.UpdateStage,
		getDomainName:             client.GetDomainName,
		createDomainName:          client.CreateDomainName,
		updateDomainName:          client.UpdateDomainName,
		deleteDomainName:          client.DeleteDomainName,
		getBasePathMappings:       client.GetBasePathMappings,
		createBasePathMapping:     client.CreateBasePathMapping,
		deleteBasePathMapping:     client.DeleteBasePathMapping,
	}
}

func (c apiGatewayCalls) GetRestApi(ctx context.Context, in *apigateway.GetRestApiInput, optFns ...func(*apigateway.Options)) (*apigateway.GetRestApiOutput, error) {
	return c.getRestApi(ctx, in, optFns...)
}

func (c apiGatewayCalls) CreateRestApi(ctx context.Context, in *apigateway.CreateRestApiInput, optFns ...func(*apigateway.Options)) (*apigateway.CreateRestApiOutput, error) {
	return c.createRestApi(ctx, in, optFns...)
}

func (c apiGatewayCalls) TagResource(ctx context.Context, in *apigateway.TagResourceInput, optFns ...func(*apigateway.Options)) (*apigateway.TagResourceOutput, error) {
	return c.tagResource(ctx, in, optFns...)
}

func (c apiGatewayCalls) DeleteRestApi(ctx context.Context, in *apigateway.DeleteRestApiInput, optFns ...func(*apigateway.Options)) (*apigateway.DeleteRestApiOutput, error) {
	return c.deleteRestApi(ctx, in, optFns...)
}

func (c apiGatewayCalls) GetResources(ctx context.Context, in *apigateway.GetResourcesInput, optFns ...func(*apigateway.Options)) (*apigateway.GetResourcesOutput, error) {
	return c.getResources(ctx, in, optFns...)
}

func (c apiGatewayCalls) CreateResource(ctx context.Context, in *apigateway.CreateResourceInput, optFns ...func(*apigateway.Options)) (*apigateway.CreateResourceOutput, error) {
	return c.createResource(ctx, in, optFns...)
}

func (c apiGatewayCalls) DeleteResource(ctx context.Context, in *apigateway.DeleteResourceInput, optFns ...func(*apigateway.Options)) (*apigateway.DeleteResourceOutput, error) {
	return c.deleteResource(ctx, in, optFns...)
}

func (c apiGatewayCalls) GetMethod(ctx context.Context, in *apigateway.GetMethodInput, optFns ...func(*apigateway.Options)) (*apigateway.GetMethodOutput, error) {
	return c.getMethod(ctx, in, optFns...)
}

func (c apiGatewayCalls) PutMethod(ctx context.Context, in *apigateway.PutMethodInput, optFns ...func(*apigateway.Options)) (*apigateway.PutMethodOutput, error) {
	return c.putMethod(ctx, in, optFns...)
}

func (c apiGatewayCalls) DeleteMethod(ctx context.Context, in *apigateway.DeleteMethodInput, optFns ...func(*apigateway.Options)) (*apigateway.DeleteMethodOutput, error) {
	return c.deleteMethod(ctx, in, optFns...)
}

func (c apiGatewayCalls) PutIntegration(ctx context.Context, in *apigateway.PutIntegrationInput, optFns ...func(*apigateway.Options)) (*apigateway.PutIntegrationOutput, error) {
	return c.putIntegration(ctx, in, optFns...)
}

func (c apiGatewayCalls) GetMethodResponse(ctx context.Context, in *apigateway.GetMethodResponseInput, optFns ...func(*apigateway.Options)) (*apigateway.GetMethodResponseOutput, error) {
	return c.getMethodResponse(ctx, in, optFns...)
}

func (c apiGatewayCalls) PutMethodResponse(ctx context.Context, in *apigateway.PutMethodResponseInput, optFns ...func(*apigateway.Options)) (*apigateway.PutMethodResponseOutput, error) {
	return c.putMethodResponse(ctx, in, optFns...)
}

func (c apiGatewayCalls) DeleteMethodResponse(ctx context.Context, in *apigateway.DeleteMethodResponseInput, optFns ...func(*apigateway.Options)) (*apigateway.DeleteMethodResponseOutput, error) {
	return c.deleteMethodResponse(ctx, in, optFns...)
}

func (c apiGatewayCalls) GetIntegrationResponse(ctx context.Context, in *apigateway.GetIntegrationResponseInput, optFns ...func(*apigateway.Options)) (*apigateway.GetIntegrationResponseOutput, error) {
	return c.getIntegrationResponse(ctx, in, optFns...)
}

func (c apiGatewayCalls) PutIntegrationResponse(ctx context.Context, in *apigateway.PutIntegrationResponseInput, optFns ...func(*apigateway.Options)) (*apigateway.PutIntegrationResponseOutput, error) {
	return c.putIntegrationResponse(ctx, in, optFns...)
}

func (c apiGatewayCalls) DeleteIntegrationResponse(ctx context.Context, in *apigateway.DeleteIntegrationResponseInput, optFns ...func(*apigateway.Options)) (*apigateway.DeleteIntegrationResponseOutput, error) {
	return c.deleteIntegrationResponse(ctx, in, optFns...)
}

func (c apiGatewayCalls) GetStage(ctx context.Context, in *apigateway.GetStageInput, optFns ...func(*apigateway.Options)) (*apigateway.GetStageOutput, error) {
	return c.getStage(ctx, in, optFns...)
}

func (c apiGatewayCalls) CreateDeployment(ctx context.Context, in *apigateway.CreateDeploymentInput, optFns ...func(*apigateway.Options)) (*apigateway.CreateDeploymentOutput, error) {
	return c.createDeployment(ctx, in, optFns...)
}

func (c apiGatewayCalls) UpdateStage(ctx context.Context, in *apigateway.UpdateStageInput, optFns ...func(*apigateway.Options)) (*apigateway.UpdateStageOutput, error) {
	return c.updateStage(ctx, in, optFns...)
}

func (c apiGatewayCalls) GetDomainName(ctx context.Context, in *apigateway.GetDomainNameInput, optFns ...func(*apigateway.Options)) (*apigateway.GetDomainNameOutput, error) {
	return c.getDomainName(ctx, in, optFns...)
}

func (c apiGatewayCalls) CreateDomainName(ctx context.Context, in *apigateway.CreateDomainNameInput, optFns ...func(*apigateway.Options)) (*apigateway.CreateDomainNameOutput, error) {
	return c.createDomainName(ctx, in, optFns...)
}

func (c apiGatewayCalls) UpdateDomainName(ctx context.Context, in *apigateway.UpdateDomainNameInput, optFns ...func(*apigateway.Options)) (*apigateway.UpdateDomainNameOutput, error) {
	return c.updateDomainName(ctx, in, optFns...)
}

func (c apiGatewayCalls) DeleteDomainName(ctx context.Context, in *apigateway.DeleteDomainNameInput, optFns ...func(*apigateway.Options)) (*apigateway.DeleteDomainNameOutput, error) {
	return c.deleteDomainName(ctx, in, optFns...)
}

func (c apiGatewayCalls) GetBasePathMappings(ctx context.Context, in *apigateway.GetBasePathMappingsInput, optFns ...func(*apigateway.Options)) (*apigateway.GetBasePathMappingsOutput, error) {
	return c.getBasePathMappings(ctx, in, optFns...)
}

func (c apiGatewayCalls) CreateBasePathMapping(ctx context.Context, in *apigateway.CreateBasePathMappingInput, optFns ...func(*apigateway.Options)) (*apigateway.CreateBasePathMappingOutput, error) {
	return c.createBasePathMapping(ctx, in, optFns...)
}

func (c apiGatewayCalls) DeleteBasePathMapping(ctx context.Context, in *apigateway.DeleteBasePathMappingInput, optFns ...func(*apigateway.Options)) (*apigateway.DeleteBasePathMappingOutput, error) {
	return c.deleteBasePathMapping(ctx, in, optFns...)
}

type routingCalls struct {
	listRoutingRules  func(context.Context, *apigatewayv2.ListRoutingRulesInput, ...func(*apigatewayv2.Options)) (*apigatewayv2.ListRoutingRulesOutput, error)
	createRoutingRule func(context.Context, *apigatewayv2.CreateRoutingRuleInput, ...func(*apigatewayv2.Options)) (*apigatewayv2.CreateRoutingRuleOutput, error)
	putRoutingRule    func(context.Context, *apigatewayv2.PutRoutingRuleInput, ...func(*apigatewayv2.Options)) (*apigatewayv2.PutRoutingRuleOutput, error)
	deleteRoutingRule func(context.Context, *apigatewayv2.DeleteRoutingRuleInput, ...func(*apigatewayv2.Options)) (*apigatewayv2.DeleteRoutingRuleOutput, error)
}

func newRoutingCalls(client *apigatewayv2.Client) routingCalls {
	return routingCalls{
		listRoutingRules:  client.ListRoutingRules,
		createRoutingRule: client.CreateRoutingRule,
		putRoutingRule:    client.PutRoutingRule,
		deleteRoutingRule: client.DeleteRoutingRule,
	}
}

func (c routingCalls) ListRoutingRules(ctx context.Context, in *apigatewayv2.ListRoutingRulesInput, optFns ...func(*apigatewayv2.Options)) (*apigatewayv2.ListRoutingRulesOutput, error) {
	return c.listRoutingRules(ctx, in, optFns...)
}

func (c routingCalls) CreateRoutingRule(ctx context.Context, in *apigatewayv2.CreateRoutingRuleInput, optFns ...func(*apigatewayv2.Options)) (*apigatewayv2.CreateRoutingRuleOutput, error) {
	return c.createRoutingRule(ctx, in, optFns...)
}

func (c routingCalls) PutRoutingRule(ctx context.Context, in *apigatewayv2.PutRoutingRuleInput, optFns ...func(*apigatewayv2.Options)) (*apigatewayv2.PutRoutingRuleOutput, error) {
	return c.putRoutingRule(ctx, in, optFns...)
}

func (c routingCalls) DeleteRoutingRule(ctx context.Context, in *apigatewayv2.DeleteRoutingRuleInput, optFns ...func(*apigatewayv2.Options)) (*apigatewayv2.DeleteRoutingRuleOutput, error) {
	return c.deleteRoutingRule(ctx, in, optFns...)
}
