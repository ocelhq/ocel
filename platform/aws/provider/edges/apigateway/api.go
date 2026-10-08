package apigateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	agtypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"

	"github.com/ocelhq/ocel/pkg/router"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

const (
	anyMethod = "ANY"
	getMethod = "GET"

	rootPath      = "/"
	proxyPathPart = "{proxy+}"
	probeParent   = ".well-known"
	probePathPart = "ocel-edge"

	routerHeaderParameter = "method.response.header." + router.HeaderRouter

	proxyPathParameter            = "method.request.path.proxy"
	integrationProxyPathParameter = "integration.request.path.proxy"
)

type apiSpec struct {
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

func createAPI(ctx context.Context, c Clients, spec apiSpec) (string, error) {
	out, err := c.APIGateway.CreateRestApi(ctx, &apigateway.CreateRestApiInput{
		Name:             aws.String(spec.name),
		Description:      aws.String("Ocel serves this project's requests through this API; the stage variables name the release in effect."),
		BinaryMediaTypes: []string{"*/*"},
		EndpointConfiguration: &agtypes.EndpointConfiguration{
			Types: []agtypes.EndpointType{agtypes.EndpointTypeRegional},
		},
	})
	if err != nil {
		return "", createAPIError(spec.name, err)
	}
	return aws.ToString(out.Id), nil
}

func shapeAPI(ctx context.Context, c Clients, spec apiSpec, id string) error {
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
		if err := putRootFunctionRoute(ctx, c, spec, id, resources[path]); err != nil {
			return err
		}
	}
	known, err := ensureResource(ctx, c, id, resources, rootPath, probeParent)
	if err != nil {
		return err
	}
	probe, err := ensureResource(ctx, c, id, resources, known, probePathPart)
	if err != nil {
		return err
	}
	if err := putProbeRoute(ctx, c, id, resources[probe]); err != nil {
		return err
	}
	return publish(ctx, c, id)
}

var pathPartPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func splitStaticPrefix(prefix string) ([]string, bool) {
	segments := strings.Split(strings.Trim(prefix, rootPath), rootPath)
	for _, segment := range segments {
		if !pathPartPattern.MatchString(segment) {
			return nil, false
		}
	}
	return segments, true
}

type staticRoutes struct {
	Reshaped    bool
	Fingerprint string
}

func routeStatic(ctx context.Context, c Clients, spec apiSpec, id string, prefixes []string) (staticRoutes, error) {
	resources, err := apiResources(ctx, c, id)
	if err != nil {
		return staticRoutes{}, err
	}
	known := len(resources)
	reshaped := false
	kept := map[string]bool{
		rootPath:                 true,
		rootPath + proxyPathPart: true,
		rootPath + probeParent:   true,
		rootPath + probeParent + rootPath + probePathPart: true,
	}
	var served []string
	if spec.assetBucket != "" {
		for _, segments := range selectOutermostPrefixes(prefixes) {
			parent := rootPath
			for i, segment := range segments {
				if parent, err = ensureResource(ctx, c, id, resources, parent, segment); err != nil {
					return staticRoutes{}, err
				}
				kept[parent] = true
				if err := putRootFunctionRoute(ctx, c, spec, id, resources[parent]); err != nil {
					return staticRoutes{}, err
				}
				if i == len(segments)-1 {
					break
				}
				rest, err := ensureResource(ctx, c, id, resources, parent, proxyPathPart)
				if err != nil {
					return staticRoutes{}, err
				}
				kept[rest] = true
				if err := putRootFunctionRoute(ctx, c, spec, id, resources[rest]); err != nil {
					return staticRoutes{}, err
				}
				stale, err := removeStaticMethod(ctx, c, id, resources[rest])
				if err != nil {
					return staticRoutes{}, err
				}
				reshaped = reshaped || stale
			}
			leaf, err := ensureResource(ctx, c, id, resources, parent, proxyPathPart)
			if err != nil {
				return staticRoutes{}, err
			}
			kept[leaf] = true
			served = append(served, rootPath+strings.Join(segments, rootPath)+rootPath)
			if err := putStaticRoute(ctx, c, spec, id, resources[leaf], served[len(served)-1]); err != nil {
				return staticRoutes{}, err
			}
		}
	}
	reshaped = reshaped || len(resources) != known
	var removed []string
	for _, path := range slices.Sorted(maps.Keys(resources)) {
		if kept[path] || slices.ContainsFunc(removed, func(gone string) bool { return strings.HasPrefix(path, gone+rootPath) }) {
			continue
		}
		if _, err := c.APIGateway.DeleteResource(ctx, &apigateway.DeleteResourceInput{
			RestApiId:  aws.String(id),
			ResourceId: aws.String(resources[path]),
		}); err != nil && !isNotFound(err) {
			return staticRoutes{}, fmt.Errorf("remove %s, which no static route of the release needs, from REST API %s: %w", path, id, err)
		}
		removed = append(removed, path)
		reshaped = true
	}
	return staticRoutes{Reshaped: reshaped, Fingerprint: fingerprintRoutes(served)}, nil
}

func selectOutermostPrefixes(prefixes []string) [][]string {
	routable := map[string][]string{}
	for _, prefix := range prefixes {
		if segments, ok := splitStaticPrefix(prefix); ok {
			routable[rootPath+strings.Join(segments, rootPath)+rootPath] = segments
		}
	}
	var outermost [][]string
	var kept []string
	for _, prefix := range slices.Sorted(maps.Keys(routable)) {
		if slices.ContainsFunc(kept, func(outer string) bool { return strings.HasPrefix(prefix, outer) }) {
			continue
		}
		kept = append(kept, prefix)
		outermost = append(outermost, routable[prefix])
	}
	return outermost
}

func removeStaticMethod(ctx context.Context, c Clients, api, resource string) (bool, error) {
	if _, err := c.APIGateway.GetMethod(ctx, &apigateway.GetMethodInput{
		RestApiId:  aws.String(api),
		ResourceId: aws.String(resource),
		HttpMethod: aws.String(getMethod),
	}); err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("read the static-asset method on REST API %s: %w", api, err)
	}
	if _, err := c.APIGateway.DeleteMethod(ctx, &apigateway.DeleteMethodInput{
		RestApiId:  aws.String(api),
		ResourceId: aws.String(resource),
		HttpMethod: aws.String(getMethod),
	}); err != nil && !isNotFound(err) {
		return false, fmt.Errorf("remove the static-asset method of a prefix the release no longer states from REST API %s: %w", api, err)
	}
	return true, nil
}

func fingerprintRoutes(served []string) string {
	if len(served) == 0 {
		return unsetVariable
	}
	sorted := slices.Sorted(slices.Values(served))
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return hex.EncodeToString(sum[:8])
}

func readStageVariable(ctx context.Context, c Clients, api, name string) (string, error) {
	stage, err := c.APIGateway.GetStage(ctx, &apigateway.GetStageInput{
		RestApiId: aws.String(api),
		StageName: aws.String(stageName),
	})
	if err != nil {
		if isNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("read the %s stage of REST API %s: %w", stageName, api, err)
	}
	return stage.Variables[name], nil
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
	var conflict *agtypes.ConflictException
	if _, err := c.APIGateway.PutMethod(ctx, in); err != nil && !errors.As(err, &conflict) {
		return err
	}
	return nil
}

func ensureMethodResponse(ctx context.Context, c Clients, in *apigateway.PutMethodResponseInput) error {
	current, err := c.APIGateway.GetMethodResponse(ctx, &apigateway.GetMethodResponseInput{
		RestApiId:  in.RestApiId,
		ResourceId: in.ResourceId,
		HttpMethod: in.HttpMethod,
		StatusCode: in.StatusCode,
	})
	switch {
	case err == nil && maps.Equal(current.ResponseParameters, in.ResponseParameters):
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
	current, err := c.APIGateway.GetIntegrationResponse(ctx, &apigateway.GetIntegrationResponseInput{
		RestApiId:  in.RestApiId,
		ResourceId: in.ResourceId,
		HttpMethod: in.HttpMethod,
		StatusCode: in.StatusCode,
	})
	switch {
	case err == nil && maps.Equal(current.ResponseParameters, in.ResponseParameters):
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

func putRootFunctionRoute(ctx context.Context, c Clients, spec apiSpec, api, resource string) error {
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
		Credentials:           aws.String(spec.role),
		Uri:                   aws.String(entryURI(spec)),
		ResponseTransferMode:  agtypes.ResponseTransferModeStream,
		TimeoutInMillis:       aws.Int32(int32(awsports.RequestTimeout.Milliseconds())),
	}); err != nil {
		return fmt.Errorf("point REST API %s at the root function: %w", api, err)
	}
	return nil
}

func putProbeRoute(ctx context.Context, c Clients, api, resource string) error {
	if err := ensureMethod(ctx, c, &apigateway.PutMethodInput{
		RestApiId:         aws.String(api),
		ResourceId:        aws.String(resource),
		HttpMethod:        aws.String(getMethod),
		AuthorizationType: aws.String("NONE"),
	}); err != nil {
		return fmt.Errorf("open the liveness probe method on REST API %s: %w", api, err)
	}
	if _, err := c.APIGateway.PutIntegration(ctx, &apigateway.PutIntegrationInput{
		RestApiId:        aws.String(api),
		ResourceId:       aws.String(resource),
		HttpMethod:       aws.String(getMethod),
		Type:             agtypes.IntegrationTypeMock,
		RequestTemplates: map[string]string{"application/json": `{"statusCode": 200}`},
	}); err != nil {
		return fmt.Errorf("have REST API %s answer the liveness probe itself: %w", api, err)
	}
	if err := ensureMethodResponse(ctx, c, &apigateway.PutMethodResponseInput{
		RestApiId:          aws.String(api),
		ResourceId:         aws.String(resource),
		HttpMethod:         aws.String(getMethod),
		StatusCode:         aws.String("200"),
		ResponseParameters: map[string]bool{routerHeaderParameter: true},
	}); err != nil {
		return fmt.Errorf("declare the liveness probe's router header on REST API %s: %w", api, err)
	}
	if err := ensureIntegrationResponse(ctx, c, &apigateway.PutIntegrationResponseInput{
		RestApiId:          aws.String(api),
		ResourceId:         aws.String(resource),
		HttpMethod:         aws.String(getMethod),
		StatusCode:         aws.String("200"),
		ResponseParameters: map[string]string{routerHeaderParameter: "'" + routerHeaderValue + "'"},
	}); err != nil {
		return fmt.Errorf("set the liveness probe's router header on REST API %s: %w", api, err)
	}
	return nil
}

func putStaticRoute(ctx context.Context, c Clients, spec apiSpec, api, resource, prefix string) error {
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
		Credentials:           aws.String(spec.role),
		Uri:                   aws.String(staticURI(spec, prefix)),
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
			routerHeaderParameter:                   true,
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
			routerHeaderParameter:                   "'" + routerHeaderValue + "'",
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
		in.Variables = unsetVariables()
	}
	if _, err := c.APIGateway.CreateDeployment(ctx, in); err != nil {
		return fmt.Errorf("deploy REST API %s to its %s stage: %w", api, stageName, err)
	}
	return nil
}

func unsetVariables() map[string]string {
	return map[string]string{
		entryVariable:  unsetVariable,
		routesVariable: unsetVariable,
		assetsVariable: unsetVariable,
	}
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

func entryURI(spec apiSpec) string {
	return fmt.Sprintf(
		"arn:aws:apigateway:%s:lambda:path/2021-11-15/functions/arn:aws:lambda:%s:%s:function:${stageVariables.%s}/response-streaming-invocations",
		spec.region, spec.region, spec.account, entryVariable,
	)
}

func staticURI(spec apiSpec, prefix string) string {
	return fmt.Sprintf(
		"arn:aws:apigateway:%s:s3:path/%s/${stageVariables.%s}%s{proxy}",
		spec.region, spec.assetBucket, assetsVariable, prefix,
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
