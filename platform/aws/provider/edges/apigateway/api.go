package apigateway

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	agtypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"
)

const (
	anyMethod = "ANY"
	getMethod = "GET"

	rootPath       = "/"
	proxyPathPart  = "{proxy+}"
	staticParent   = "_next"
	staticPathPart = "static"

	edgeHeaderParameter = "method.response.header." + EdgeHeader

	proxyPathParameter            = "method.request.path.proxy"
	integrationProxyPathParameter = "integration.request.path.proxy"
)

type apiPlan struct {
	name        string
	region      string
	account     string
	role        string
	assetBucket string
}

func restAPIs(ctx context.Context, c Clients) (map[string]string, error) {
	names := map[string]string{}
	var position *string
	for {
		page, err := c.APIGateway.GetRestApis(ctx, &apigateway.GetRestApisInput{Position: position})
		if err != nil {
			return nil, fmt.Errorf("list the REST APIs this account already serves: %w", err)
		}
		for _, api := range page.Items {
			names[aws.ToString(api.Id)] = aws.ToString(api.Name)
		}
		if aws.ToString(page.Position) == "" {
			return names, nil
		}
		position = page.Position
	}
}

func findAPI(ctx context.Context, c Clients, name string) (string, bool, error) {
	names, err := restAPIs(ctx, c)
	if err != nil {
		return "", false, err
	}
	for _, id := range slices.Sorted(maps.Keys(names)) {
		if names[id] == name {
			return id, true, nil
		}
	}
	return "", false, nil
}

func findAPIs(ctx context.Context, c Clients, wanted []string) ([]string, error) {
	names, err := restAPIs(ctx, c)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, id := range slices.Sorted(maps.Keys(names)) {
		if slices.Contains(wanted, names[id]) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func createAPI(ctx context.Context, c Clients, plan apiPlan) (string, error) {
	out, err := c.APIGateway.CreateRestApi(ctx, &apigateway.CreateRestApiInput{
		Name:             aws.String(plan.name),
		Description:      aws.String("Ocel serves this project's requests through this API; the stage variables name the release in effect."),
		BinaryMediaTypes: []string{"*/*"},
		EndpointConfiguration: &agtypes.EndpointConfiguration{
			Types: []agtypes.EndpointType{agtypes.EndpointTypeRegional},
		},
	})
	if err != nil {
		return "", createAPIError(plan.name, err)
	}
	return aws.ToString(out.Id), nil
}

func shapeAPI(ctx context.Context, c Clients, plan apiPlan, id string) error {
	resources, err := apiResources(ctx, c, id)
	if err != nil {
		return err
	}
	if _, ok := resources[rootPath]; !ok {
		return fmt.Errorf("REST API %s has no root resource", id)
	}
	proxy, err := ensureResource(ctx, c, id, resources, rootPath, proxyPathPart)
	if err != nil {
		return err
	}
	for _, path := range []string{rootPath, proxy} {
		if err := putEntryRoute(ctx, c, plan, id, resources[path]); err != nil {
			return err
		}
	}
	if plan.assetBucket != "" {
		next, err := ensureResource(ctx, c, id, resources, rootPath, staticParent)
		if err != nil {
			return err
		}
		static, err := ensureResource(ctx, c, id, resources, next, staticPathPart)
		if err != nil {
			return err
		}
		staticProxy, err := ensureResource(ctx, c, id, resources, static, proxyPathPart)
		if err != nil {
			return err
		}
		if err := putStaticRoute(ctx, c, plan, id, resources[staticProxy]); err != nil {
			return err
		}
	}
	return publish(ctx, c, id)
}

func apiResources(ctx context.Context, c Clients, api string) (map[string]string, error) {
	byPath := map[string]string{}
	var position *string
	for {
		page, err := c.APIGateway.GetResources(ctx, &apigateway.GetResourcesInput{
			RestApiId: aws.String(api),
			Position:  position,
		})
		if err != nil {
			return nil, fmt.Errorf("read the resources of REST API %s: %w", api, err)
		}
		for _, resource := range page.Items {
			byPath[aws.ToString(resource.Path)] = aws.ToString(resource.Id)
		}
		if aws.ToString(page.Position) == "" {
			return byPath, nil
		}
		position = page.Position
	}
}

func ensureResource(ctx context.Context, c Clients, api string, resources map[string]string, parent, pathPart string) (string, error) {
	path := strings.TrimSuffix(parent, rootPath) + rootPath + pathPart
	if _, found := resources[path]; found {
		return path, nil
	}
	out, err := c.APIGateway.CreateResource(ctx, &apigateway.CreateResourceInput{
		RestApiId: aws.String(api),
		ParentId:  aws.String(resources[parent]),
		PathPart:  aws.String(pathPart),
	})
	if err != nil {
		return "", fmt.Errorf("add the resource %q to REST API %s: %w", pathPart, api, err)
	}
	resources[path] = aws.ToString(out.Id)
	return path, nil
}

func ensureMethod(ctx context.Context, c Clients, in *apigateway.PutMethodInput) error {
	_, err := c.APIGateway.GetMethod(ctx, &apigateway.GetMethodInput{
		RestApiId:  in.RestApiId,
		ResourceId: in.ResourceId,
		HttpMethod: in.HttpMethod,
	})
	if err == nil {
		return nil
	}
	if !isNotFound(err) {
		return err
	}
	var held *agtypes.ConflictException
	if _, err := c.APIGateway.PutMethod(ctx, in); err != nil && !errors.As(err, &held) {
		return err
	}
	return nil
}

func ensureMethodResponse(ctx context.Context, c Clients, in *apigateway.PutMethodResponseInput) error {
	held, err := c.APIGateway.GetMethodResponse(ctx, &apigateway.GetMethodResponseInput{
		RestApiId:  in.RestApiId,
		ResourceId: in.ResourceId,
		HttpMethod: in.HttpMethod,
		StatusCode: in.StatusCode,
	})
	switch {
	case err == nil && maps.Equal(held.ResponseParameters, in.ResponseParameters):
		return nil
	case err == nil:
		if _, err := c.APIGateway.DeleteMethodResponse(ctx, &apigateway.DeleteMethodResponseInput{
			RestApiId:  in.RestApiId,
			ResourceId: in.ResourceId,
			HttpMethod: in.HttpMethod,
			StatusCode: in.StatusCode,
		}); err != nil && !isNotFound(err) {
			return err
		}
	case !isNotFound(err):
		return err
	}
	var conflict *agtypes.ConflictException
	if _, err := c.APIGateway.PutMethodResponse(ctx, in); err != nil && !errors.As(err, &conflict) {
		return err
	}
	return nil
}

func ensureIntegrationResponse(ctx context.Context, c Clients, in *apigateway.PutIntegrationResponseInput) error {
	held, err := c.APIGateway.GetIntegrationResponse(ctx, &apigateway.GetIntegrationResponseInput{
		RestApiId:  in.RestApiId,
		ResourceId: in.ResourceId,
		HttpMethod: in.HttpMethod,
		StatusCode: in.StatusCode,
	})
	switch {
	case err == nil && maps.Equal(held.ResponseParameters, in.ResponseParameters):
		return nil
	case err == nil:
		if _, err := c.APIGateway.DeleteIntegrationResponse(ctx, &apigateway.DeleteIntegrationResponseInput{
			RestApiId:  in.RestApiId,
			ResourceId: in.ResourceId,
			HttpMethod: in.HttpMethod,
			StatusCode: in.StatusCode,
		}); err != nil && !isNotFound(err) {
			return err
		}
	case !isNotFound(err):
		return err
	}
	var conflict *agtypes.ConflictException
	if _, err := c.APIGateway.PutIntegrationResponse(ctx, in); err != nil && !errors.As(err, &conflict) {
		return err
	}
	return nil
}

func putEntryRoute(ctx context.Context, c Clients, plan apiPlan, api, resource string) error {
	if err := ensureMethod(ctx, c, &apigateway.PutMethodInput{
		RestApiId:         aws.String(api),
		ResourceId:        aws.String(resource),
		HttpMethod:        aws.String(anyMethod),
		AuthorizationType: aws.String("NONE"),
	}); err != nil {
		return fmt.Errorf("open the entry method on REST API %s: %w", api, err)
	}
	if _, err := c.APIGateway.PutIntegration(ctx, &apigateway.PutIntegrationInput{
		RestApiId:             aws.String(api),
		ResourceId:            aws.String(resource),
		HttpMethod:            aws.String(anyMethod),
		Type:                  agtypes.IntegrationTypeAwsProxy,
		IntegrationHttpMethod: aws.String("POST"),
		Credentials:           aws.String(plan.role),
		Uri:                   aws.String(entryURI(plan)),
		ResponseTransferMode:  agtypes.ResponseTransferModeStream,
	}); err != nil {
		return fmt.Errorf("point REST API %s at the entry function: %w", api, err)
	}
	return nil
}

func putStaticRoute(ctx context.Context, c Clients, plan apiPlan, api, resource string) error {
	if err := ensureMethod(ctx, c, &apigateway.PutMethodInput{
		RestApiId:         aws.String(api),
		ResourceId:        aws.String(resource),
		HttpMethod:        aws.String(getMethod),
		AuthorizationType: aws.String("NONE"),
		RequestParameters: map[string]bool{proxyPathParameter: true},
	}); err != nil {
		return fmt.Errorf("open the static-asset method on REST API %s: %w", api, err)
	}
	if _, err := c.APIGateway.PutIntegration(ctx, &apigateway.PutIntegrationInput{
		RestApiId:             aws.String(api),
		ResourceId:            aws.String(resource),
		HttpMethod:            aws.String(getMethod),
		Type:                  agtypes.IntegrationTypeAws,
		IntegrationHttpMethod: aws.String(getMethod),
		Credentials:           aws.String(plan.role),
		Uri:                   aws.String(staticURI(plan)),
		RequestParameters:     map[string]string{integrationProxyPathParameter: proxyPathParameter},
	}); err != nil {
		return fmt.Errorf("point REST API %s at the release's static assets: %w", api, err)
	}
	if err := ensureMethodResponse(ctx, c, &apigateway.PutMethodResponseInput{
		RestApiId:  aws.String(api),
		ResourceId: aws.String(resource),
		HttpMethod: aws.String(getMethod),
		StatusCode: aws.String("200"),
		ResponseParameters: map[string]bool{
			edgeHeaderParameter:                     true,
			"method.response.header.Content-Type":   true,
			"method.response.header.Cache-Control":  true,
			"method.response.header.Content-Length": true,
		},
	}); err != nil {
		return fmt.Errorf("declare the static-asset response headers on REST API %s: %w", api, err)
	}
	if err := ensureIntegrationResponse(ctx, c, &apigateway.PutIntegrationResponseInput{
		RestApiId:  aws.String(api),
		ResourceId: aws.String(resource),
		HttpMethod: aws.String(getMethod),
		StatusCode: aws.String("200"),
		ResponseParameters: map[string]string{
			edgeHeaderParameter:                     "'" + edgeHeaderValue + "'",
			"method.response.header.Content-Type":   "integration.response.header.Content-Type",
			"method.response.header.Cache-Control":  "integration.response.header.Cache-Control",
			"method.response.header.Content-Length": "integration.response.header.Content-Length",
		},
	}); err != nil {
		return fmt.Errorf("set the static-asset response headers on REST API %s: %w", api, err)
	}
	return nil
}

func publish(ctx context.Context, c Clients, api string) error {
	staged, err := stagePresent(ctx, c, api)
	if err != nil {
		return err
	}
	in := &apigateway.CreateDeploymentInput{
		RestApiId: aws.String(api),
		StageName: aws.String(stageName),
	}
	if !staged {
		in.Variables = map[string]string{
			entryVariable:  unsetVariable,
			assetsVariable: unsetVariable,
		}
	}
	if _, err := c.APIGateway.CreateDeployment(ctx, in); err != nil {
		return fmt.Errorf("deploy REST API %s to its %s stage: %w", api, stageName, err)
	}
	return nil
}

func stagePresent(ctx context.Context, c Clients, api string) (bool, error) {
	_, err := c.APIGateway.GetStage(ctx, &apigateway.GetStageInput{
		RestApiId: aws.String(api),
		StageName: aws.String(stageName),
	})
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("read the %s stage of REST API %s: %w", stageName, api, err)
	}
	return true, nil
}

func deleteAPI(ctx context.Context, c Clients, id string) error {
	if _, err := c.APIGateway.DeleteRestApi(ctx, &apigateway.DeleteRestApiInput{RestApiId: aws.String(id)}); err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("delete REST API %s: %w", id, err)
	}
	return nil
}

func entryURI(plan apiPlan) string {
	return fmt.Sprintf(
		"arn:aws:apigateway:%s:lambda:path/2021-11-15/functions/arn:aws:lambda:%s:%s:function:${stageVariables.%s}/response-streaming-invocations",
		plan.region, plan.region, plan.account, entryVariable,
	)
}

func staticURI(plan apiPlan) string {
	return fmt.Sprintf(
		"arn:aws:apigateway:%s:s3:path/%s/${stageVariables.%s}/%s/%s/{proxy}",
		plan.region, plan.assetBucket, assetsVariable, staticParent, staticPathPart,
	)
}

func basePathMappings(ctx context.Context, c Clients, hostname string) ([]agtypes.BasePathMapping, error) {
	var (
		out      []agtypes.BasePathMapping
		position *string
	)
	for {
		page, err := c.APIGateway.GetBasePathMappings(ctx, &apigateway.GetBasePathMappingsInput{
			DomainName: aws.String(hostname),
			Position:   position,
		})
		if err != nil {
			if isNotFound(err) {
				return out, nil
			}
			return nil, fmt.Errorf("read what %s is mapped to: %w", hostname, err)
		}
		out = append(out, page.Items...)
		if aws.ToString(page.Position) == "" {
			return out, nil
		}
		position = page.Position
	}
}
