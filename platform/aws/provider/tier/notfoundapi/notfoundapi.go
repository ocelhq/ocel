package notfoundapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	agtypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/router"
)

const (
	Part    = "apigateway-preview"
	Summary = "the REST API that answers 404 for every preview host on the API Gateway wildcard that no preview claims"

	StageName = "live"

	routerValue = "api-gateway"

	notFoundWord = "not-found"

	KindRestAPI = "AWS::ApiGateway::RestApi"
	kindStage   = "AWS::ApiGateway::Stage"
)

var contentTypes = []string{
	"application/json",
	"text/plain",
	"text/html",
	"application/x-www-form-urlencoded",
	"multipart/form-data",
	"application/octet-stream",
}

type API interface {
	CreateRestApi(ctx context.Context, in *apigateway.CreateRestApiInput, optFns ...func(*apigateway.Options)) (*apigateway.CreateRestApiOutput, error)
	GetRestApi(ctx context.Context, in *apigateway.GetRestApiInput, optFns ...func(*apigateway.Options)) (*apigateway.GetRestApiOutput, error)
	PutRestApi(ctx context.Context, in *apigateway.PutRestApiInput, optFns ...func(*apigateway.Options)) (*apigateway.PutRestApiOutput, error)
	CreateDeployment(ctx context.Context, in *apigateway.CreateDeploymentInput, optFns ...func(*apigateway.Options)) (*apigateway.CreateDeploymentOutput, error)
	DeleteRestApi(ctx context.Context, in *apigateway.DeleteRestApiInput, optFns ...func(*apigateway.Options)) (*apigateway.DeleteRestApiOutput, error)
}

func NameFor(namespace string, tier environment.Tier) string {
	return naming.OcelName(namespace, tier, notFoundWord)
}

func definition(name string) ([]byte, error) {
	templates := map[string]string{}
	for _, contentType := range contentTypes {
		templates[contentType] = `{"statusCode": 404}`
	}
	header := "method.response.header." + router.HeaderRouter
	method := func(proxied bool) map[string]any {
		m := map[string]any{
			"responses": map[string]any{"404": map[string]any{
				"description": "Ocel answers 404 for a host no deployment claims.",
				"headers":     map[string]any{router.HeaderRouter: map[string]any{"schema": map[string]any{"type": "string"}}},
			}},
			"x-amazon-apigateway-integration": map[string]any{
				"type":                "mock",
				"passthroughBehavior": "when_no_templates",
				"requestTemplates":    templates,
				"responses": map[string]any{"default": map[string]any{
					"statusCode":         "404",
					"responseParameters": map[string]any{header: "'" + routerValue + "'"},
					"responseTemplates":  map[string]any{"application/json": `{"message":"Not Found"}`},
				}},
			},
		}
		if proxied {
			m["parameters"] = []any{map[string]any{"name": "proxy", "in": "path", "required": true, "schema": map[string]any{"type": "string"}}}
		}
		return m
	}
	return json.Marshal(map[string]any{
		"openapi": "3.0.1",
		"info":    map[string]any{"title": name, "version": "1"},
		"paths": map[string]any{
			"/":         map[string]any{"x-amazon-apigateway-any-method": method(false)},
			"/{proxy+}": map[string]any{"x-amazon-apigateway-any-method": method(true)},
		},
	})
}

func Digest(namespace string, tier environment.Tier) string {
	body, _ := definition(NameFor(namespace, tier))
	sum := sha256.Sum256(append(body, []byte("\n"+StageName)...))
	return hex.EncodeToString(sum[:])
}

func IDOf(resources []provider.PhysicalResource) string {
	for _, resource := range resources {
		if resource.Kind == KindRestAPI {
			return resource.ID
		}
	}
	return ""
}

func Ensure(ctx context.Context, c API, namespace string, tier environment.Tier, prior []provider.PhysicalResource) ([]provider.PhysicalResource, error) {
	name := NameFor(namespace, tier)
	body, err := definition(name)
	if err != nil {
		return nil, err
	}
	id := IDOf(prior)
	if id != "" {
		_, err := c.GetRestApi(ctx, &apigateway.GetRestApiInput{RestApiId: aws.String(id)})
		var missing *agtypes.NotFoundException
		switch {
		case errors.As(err, &missing):
			id = ""
		case err != nil:
			return nil, fmt.Errorf("read REST API %s: %w", name, err)
		}
	}
	if id == "" {
		created, err := c.CreateRestApi(ctx, &apigateway.CreateRestApiInput{
			Name:                  aws.String(name),
			Description:           aws.String("Ocel answers 404 from here for every host pointed at this account that no deployment claims."),
			EndpointConfiguration: &agtypes.EndpointConfiguration{Types: []agtypes.EndpointType{agtypes.EndpointTypeRegional}},
			Tags:                  naming.OcelTags(namespace, tier),
		})
		if err != nil {
			return nil, fmt.Errorf("create REST API %s: %w", name, err)
		}
		id = aws.ToString(created.Id)
	}
	if _, err := c.PutRestApi(ctx, &apigateway.PutRestApiInput{
		RestApiId: aws.String(id),
		Mode:      agtypes.PutModeOverwrite,
		Body:      body,
	}); err != nil {
		return nil, fmt.Errorf("write the 404 responder into REST API %s: %w", name, err)
	}
	if _, err := c.CreateDeployment(ctx, &apigateway.CreateDeploymentInput{
		RestApiId:   aws.String(id),
		StageName:   aws.String(StageName),
		Description: aws.String("Ocel: publishes the 404 responder."),
	}); err != nil {
		return nil, fmt.Errorf("publish REST API %s on stage %s: %w", name, StageName, err)
	}
	return []provider.PhysicalResource{
		{Kind: KindRestAPI, ID: id},
		{Kind: kindStage, ID: id + "/" + StageName},
	}, nil
}

func Remove(ctx context.Context, c API, recorded []provider.PhysicalResource) error {
	id := IDOf(recorded)
	if id == "" {
		return nil
	}
	var missing *agtypes.NotFoundException
	if _, err := c.DeleteRestApi(ctx, &apigateway.DeleteRestApiInput{RestApiId: aws.String(id)}); err != nil && !errors.As(err, &missing) {
		return fmt.Errorf("delete REST API %s: %w", id, err)
	}
	return nil
}

func Shape(namespace string, tier environment.Tier) []pricing.Shaped {
	return []pricing.Shaped{{Name: NameFor(namespace, tier), Type: "aws_api_gateway_rest_api", Properties: map[string]any{}}}
}
