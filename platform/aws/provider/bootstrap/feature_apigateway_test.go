package bootstrap

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"gopkg.in/yaml.v3"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type apiGatewayEdgeShape struct {
	Parameters map[string]struct {
		Type string `yaml:"Type"`
	} `yaml:"Parameters"`
	Resources map[string]struct {
		Type       string   `yaml:"Type"`
		DependsOn  []string `yaml:"DependsOn"`
		Properties struct {
			RoleName     string `yaml:"RoleName"`
			Name         string `yaml:"Name"`
			PathPart     string `yaml:"PathPart"`
			ResourceId   string `yaml:"ResourceId"`
			HttpMethod   string `yaml:"HttpMethod"`
			StageName    string `yaml:"StageName"`
			DeploymentId string `yaml:"DeploymentId"`
		} `yaml:"Properties"`
	} `yaml:"Resources"`
	Outputs map[string]struct {
		Value string `yaml:"Value"`
	} `yaml:"Outputs"`
}

func apiGatewayEdgeStack(t *testing.T, class string) (featureStack, apiGatewayEdgeShape) {
	t.Helper()
	stack := featureStackFor(FeatureAPIGatewayEdge, class, everyFeature())
	var tmpl apiGatewayEdgeShape
	if err := yaml.Unmarshal([]byte(stack.body), &tmpl); err != nil {
		t.Fatalf("template is not valid YAML: %v", err)
	}
	return stack, tmpl
}

func deploymentNamed(tmpl apiGatewayEdgeShape) string {
	for id, resource := range tmpl.Resources {
		if resource.Type == "AWS::ApiGateway::Deployment" {
			return id
		}
	}
	return ""
}

func TestAPIGatewayEdgeStandsUpWhatEveryRESTAPIInTheAccountShares(t *testing.T) {
	for _, class := range []string{ClassProduction, ClassPreview} {
		t.Run(class, func(t *testing.T) {
			held := edge.Class(class)
			_, tmpl := apiGatewayEdgeStack(t, class)

			role, ok := tmpl.Resources["EdgeInvokeRole"]
			if !ok {
				t.Fatal("template stands up no invoke role; no REST API can reach an entry function or a release's assets")
			}
			if role.Type != "AWS::IAM::Role" {
				t.Errorf("EdgeInvokeRole Type = %q, want AWS::IAM::Role", role.Type)
			}
			if got, want := role.Properties.RoleName, EdgeInvokeRoleName(held); got != want {
				t.Errorf("invoke role name = %q, want %q", got, want)
			}

			api, ok := tmpl.Resources["EdgeNotFoundApi"]
			if !ok {
				t.Fatal("template stands up no 404 responder; a host no deployment claims is answered by API Gateway rather than by Ocel")
			}
			if api.Type != "AWS::ApiGateway::RestApi" {
				t.Errorf("EdgeNotFoundApi Type = %q, want AWS::ApiGateway::RestApi", api.Type)
			}
			if got, want := api.Properties.Name, EdgeNotFoundAPIName(held); got != want {
				t.Errorf("404 responder name = %q, want %q", got, want)
			}

			proxy, ok := tmpl.Resources["EdgeNotFoundProxy"]
			if !ok {
				t.Fatal("the 404 responder holds no catch-all path, so a request for anything but the root is refused rather than answered")
			}
			if got := proxy.Properties.PathPart; got != edgeProxyPathPart {
				t.Errorf("catch-all PathPart = %q, want %q", got, edgeProxyPathPart)
			}

			root, ok := tmpl.Resources["EdgeNotFoundRootMethod"]
			if !ok {
				t.Fatal("the 404 responder answers nothing on its root")
			}
			if got := root.Properties.ResourceId; got != "EdgeNotFoundApi.RootResourceId" {
				t.Errorf("root method is attached to %q, want the API's own root resource", got)
			}
			under, ok := tmpl.Resources["EdgeNotFoundProxyMethod"]
			if !ok {
				t.Fatal("the 404 responder answers nothing under its catch-all path")
			}
			if got := under.Properties.ResourceId; got != "EdgeNotFoundProxy" {
				t.Errorf("catch-all method is attached to %q, want the catch-all resource", got)
			}
			for name, method := range map[string]string{"EdgeNotFoundRootMethod": root.Properties.HttpMethod, "EdgeNotFoundProxyMethod": under.Properties.HttpMethod} {
				if method != edgeAnyMethod {
					t.Errorf("%s answers %q, want %s: an unclaimed host is told so whatever it asked", name, method, edgeAnyMethod)
				}
			}

			deployment := deploymentNamed(tmpl)
			if deployment == "" {
				t.Fatal("nothing publishes the 404 responder's methods, so the API stands and serves nothing")
			}
			for _, method := range []string{"EdgeNotFoundRootMethod", "EdgeNotFoundProxyMethod"} {
				if !containsString(tmpl.Resources[deployment].DependsOn, method) {
					t.Errorf("the deployment does not wait on %s, so it can publish an API that holds no method", method)
				}
			}

			stage, ok := tmpl.Resources["EdgeNotFoundStage"]
			if !ok {
				t.Fatal("the 404 responder has no stage, so a routing rule has nothing to point at")
			}
			if got, want := stage.Properties.StageName, EdgeStageName; got != want {
				t.Errorf("stage name = %q, want %q", got, want)
			}
			if got := stage.Properties.DeploymentId; got != deployment {
				t.Errorf("the stage serves %q, want the deployment this template publishes, %q", got, deployment)
			}

			for _, key := range []string{OutputEdgeInvokeRoleARN, OutputEdgeNotFoundAPIID} {
				if _, ok := tmpl.Outputs[key]; !ok {
					t.Errorf("template does not output %s, which every API Gateway deploy reads off this stack", key)
				}
			}
		})
	}
}

func TestAPIGatewayEdgeReadsTheAssetBucketOffAParameter(t *testing.T) {
	stack, tmpl := apiGatewayEdgeStack(t, ClassProduction)

	if _, ok := tmpl.Parameters[paramAssetBucketARN]; !ok {
		t.Fatalf("template declares parameters %v, want the asset bucket ARN among them", tmpl.Parameters)
	}
	var handed string
	for _, p := range stack.params {
		if aws.ToString(p.ParameterKey) == paramAssetBucketARN {
			handed = aws.ToString(p.ParameterValue)
		}
	}
	if want := fixtureRefs().assetBucketARN; handed != want {
		t.Errorf("the stack is handed asset bucket ARN %q, want the one the core stack output, %q", handed, want)
	}
	if strings.Contains(stack.body, "AssetBucket.Arn") {
		t.Error("the template reaches into the core stack for the asset bucket; a feature reads its upstream from a parameter alone")
	}
}

func TestAPIGatewayEdgeRepublishesA404ResponderThatMoved(t *testing.T) {
	_, production := apiGatewayEdgeStack(t, ClassProduction)
	_, again := apiGatewayEdgeStack(t, ClassProduction)
	_, preview := apiGatewayEdgeStack(t, ClassPreview)

	if deploymentNamed(production) != deploymentNamed(again) {
		t.Error("two renders of the same responder are published under different names, so every bootstrap leaves a deployment behind")
	}
	if deploymentNamed(production) == deploymentNamed(preview) {
		t.Error("a moved responder is published under the name the old one holds, so the change is never served")
	}
}

func containsString(haystack []string, want string) bool {
	for _, got := range haystack {
		if got == want {
			return true
		}
	}
	return false
}
