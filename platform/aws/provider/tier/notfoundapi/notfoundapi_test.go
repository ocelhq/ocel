package notfoundapi_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	agtypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/notfoundapi"
)

type restAPI struct {
	name        string
	tags        map[string]string
	endpoint    []agtypes.EndpointType
	body        []byte
	deployments []string
}

type gateway struct {
	apis map[string]*restAPI
	next int
}

func newGateway() *gateway { return &gateway{apis: map[string]*restAPI{}} }

func (g *gateway) CreateRestApi(_ context.Context, in *apigateway.CreateRestApiInput, _ ...func(*apigateway.Options)) (*apigateway.CreateRestApiOutput, error) {
	g.next++
	id := "api" + string(rune('0'+g.next))
	g.apis[id] = &restAPI{name: aws.ToString(in.Name), tags: in.Tags, endpoint: in.EndpointConfiguration.Types}
	return &apigateway.CreateRestApiOutput{Id: aws.String(id)}, nil
}

func (g *gateway) GetRestApi(_ context.Context, in *apigateway.GetRestApiInput, _ ...func(*apigateway.Options)) (*apigateway.GetRestApiOutput, error) {
	if _, ok := g.apis[aws.ToString(in.RestApiId)]; !ok {
		return nil, &agtypes.NotFoundException{Message: aws.String("no api")}
	}
	return &apigateway.GetRestApiOutput{Id: in.RestApiId}, nil
}

func (g *gateway) PutRestApi(_ context.Context, in *apigateway.PutRestApiInput, _ ...func(*apigateway.Options)) (*apigateway.PutRestApiOutput, error) {
	api, ok := g.apis[aws.ToString(in.RestApiId)]
	if !ok || in.Mode != agtypes.PutModeOverwrite {
		return nil, &agtypes.NotFoundException{Message: aws.String("no api to overwrite")}
	}
	api.body = in.Body
	return &apigateway.PutRestApiOutput{}, nil
}

func (g *gateway) CreateDeployment(_ context.Context, in *apigateway.CreateDeploymentInput, _ ...func(*apigateway.Options)) (*apigateway.CreateDeploymentOutput, error) {
	api := g.apis[aws.ToString(in.RestApiId)]
	api.deployments = append(api.deployments, aws.ToString(in.StageName))
	return &apigateway.CreateDeploymentOutput{}, nil
}

func (g *gateway) DeleteRestApi(_ context.Context, in *apigateway.DeleteRestApiInput, _ ...func(*apigateway.Options)) (*apigateway.DeleteRestApiOutput, error) {
	if _, ok := g.apis[aws.ToString(in.RestApiId)]; !ok {
		return nil, &agtypes.NotFoundException{Message: aws.String("no api")}
	}
	delete(g.apis, aws.ToString(in.RestApiId))
	return &apigateway.DeleteRestApiOutput{}, nil
}

func TestEnsureMakesATaggedRegionalAPIAnsweringEveryPath404OnTheLiveStage(t *testing.T) {
	t.Parallel()
	g := newGateway()

	resources, err := notfoundapi.Ensure(context.Background(), g, "ocel", environment.TierPreview, nil)
	if err != nil {
		t.Fatalf("Ensure() = %v", err)
	}

	api := g.apis[notfoundapi.IDOf(resources)]
	if api == nil {
		t.Fatalf("resources %v name no REST API that was made", resources)
	}
	if api.name != "ocel-not-found-preview" || api.tags["ocel:managed-by"] != "ocel" || api.tags["ocel:env-tier"] != "preview" {
		t.Errorf("api = %+v, want the preview not-found API tagged as ocel's of preview, the tags the deploy credential creates under", api)
	}
	if len(api.endpoint) != 1 || api.endpoint[0] != agtypes.EndpointTypeRegional {
		t.Errorf("endpoint = %v, want regional", api.endpoint)
	}
	var definition struct {
		Paths map[string]map[string]struct {
			Integration struct {
				Type      string `json:"type"`
				Responses map[string]struct {
					StatusCode string            `json:"statusCode"`
					Parameters map[string]string `json:"responseParameters"`
				} `json:"responses"`
			} `json:"x-amazon-apigateway-integration"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(api.body, &definition); err != nil {
		t.Fatalf("definition is not JSON: %v", err)
	}
	for _, path := range []string{"/", "/{proxy+}"} {
		method, ok := definition.Paths[path]["x-amazon-apigateway-any-method"]
		if !ok {
			t.Fatalf("definition has no method on %s", path)
		}
		answer := method.Integration.Responses["default"]
		if method.Integration.Type != "mock" || answer.StatusCode != "404" || answer.Parameters["method.response.header."+router.HeaderRouter] != "'api-gateway'" {
			t.Errorf("%s answers %+v, want a mocked 404 naming the api-gateway router", path, method.Integration)
		}
	}
	if len(api.deployments) != 1 || api.deployments[0] != notfoundapi.StageName {
		t.Errorf("deployments = %v, want one on the %s stage the preview rules point at", api.deployments, notfoundapi.StageName)
	}
}

func TestEnsureAgainRewritesTheRecordedAPIRatherThanMakingAnother(t *testing.T) {
	t.Parallel()
	g := newGateway()
	first, err := notfoundapi.Ensure(context.Background(), g, "ocel", environment.TierPreview, nil)
	if err != nil {
		t.Fatalf("Ensure() = %v", err)
	}

	second, err := notfoundapi.Ensure(context.Background(), g, "ocel", environment.TierPreview, first)
	if err != nil {
		t.Fatalf("Ensure() again = %v", err)
	}

	if len(g.apis) != 1 || notfoundapi.IDOf(first) != notfoundapi.IDOf(second) {
		t.Errorf("apis %d, ids %q then %q; want the one recorded API rewritten", len(g.apis), notfoundapi.IDOf(first), notfoundapi.IDOf(second))
	}
}

func TestEnsureMakesAnotherAPIWhenTheRecordedOneIsGone(t *testing.T) {
	t.Parallel()
	g := newGateway()
	first, err := notfoundapi.Ensure(context.Background(), g, "ocel", environment.TierPreview, nil)
	if err != nil {
		t.Fatalf("Ensure() = %v", err)
	}
	delete(g.apis, notfoundapi.IDOf(first))

	second, err := notfoundapi.Ensure(context.Background(), g, "ocel", environment.TierPreview, first)
	if err != nil {
		t.Fatalf("Ensure() = %v", err)
	}
	if g.apis[notfoundapi.IDOf(second)] == nil {
		t.Errorf("resources %v name no API, want a new one in place of the one deleted by hand", second)
	}
}

func TestRemoveDeletesTheRecordedAPIAndIgnoresOneAlreadyGone(t *testing.T) {
	t.Parallel()
	g := newGateway()
	resources, err := notfoundapi.Ensure(context.Background(), g, "ocel", environment.TierPreview, nil)
	if err != nil {
		t.Fatalf("Ensure() = %v", err)
	}

	for range 2 {
		if err := notfoundapi.Remove(context.Background(), g, resources); err != nil {
			t.Fatalf("Remove() = %v", err)
		}
	}
	if len(g.apis) != 0 {
		t.Errorf("apis = %v, want none", g.apis)
	}
}
