package apigateway

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func coreSection(t *testing.T, name, body string) map[string]map[string]any {
	t.Helper()
	var parsed map[string]map[string]map[string]any
	if err := yaml.Unmarshal([]byte(name+":\n"+body), &parsed); err != nil {
		t.Fatalf("the fragment is spliced into the core template and parsed by the plan renderer, so it has to be YAML: %v\n%s", err, body)
	}
	return parsed[name]
}

func coreResources(t *testing.T, class edge.Class) map[string]map[string]any {
	t.Helper()
	return coreSection(t, "Resources", newWorld().edge().CoreStack(string(class)).Resources)
}

func TestCoreStackCarriesEverythingBootstrapUsedToCreate(t *testing.T) {
	t.Parallel()

	for _, class := range []edge.Class{edge.ClassProduction, edge.ClassPreview} {
		t.Run(string(class), func(t *testing.T) {
			t.Parallel()

			resources := coreResources(t, class)
			want := []string{
				"EdgeInvokeRole",
				"EdgeNotFoundApi",
				"EdgeNotFoundProxy",
				"EdgeNotFoundRootMethod",
				"EdgeNotFoundProxyMethod",
				deploymentOf(t, resources),
				"EdgeNotFoundStage",
			}
			assertSet(t, "the resources the fragment carries", slices.Collect(maps.Keys(resources)), want)

			role := resources["EdgeInvokeRole"]
			if role["Type"] != "AWS::IAM::Role" {
				t.Errorf("EdgeInvokeRole is a %v, want AWS::IAM::Role", role["Type"])
			}
			properties, _ := role["Properties"].(map[string]any)
			if got := properties["RoleName"]; got != invokeRoleName(class) {
				t.Errorf("RoleName = %v, want %q; every deploy in this account looks the role up by that name", got, invokeRoleName(class))
			}

			api := resources["EdgeNotFoundApi"]
			if api["Type"] != "AWS::ApiGateway::RestApi" {
				t.Errorf("EdgeNotFoundApi is a %v, want AWS::ApiGateway::RestApi", api["Type"])
			}
			properties, _ = api["Properties"].(map[string]any)
			if got := properties["Name"]; got != notFoundAPIName(class) {
				t.Errorf("Name = %v, want %q", got, notFoundAPIName(class))
			}

			stage, _ := resources["EdgeNotFoundStage"]["Properties"].(map[string]any)
			if got := stage["StageName"]; got != stageName {
				t.Errorf("StageName = %v, want %q; routing rules name that stage", got, stageName)
			}
		})
	}
}

func TestTheNotFoundMethodsAnswer404WithTheEdgeHeader(t *testing.T) {
	t.Parallel()

	resources := coreResources(t, edge.ClassProduction)
	for _, logical := range []string{"EdgeNotFoundRootMethod", "EdgeNotFoundProxyMethod"} {
		properties, _ := resources[logical]["Properties"].(map[string]any)
		if got := properties["HttpMethod"]; got != anyMethod {
			t.Errorf("%s answers %v, want %s; a host no deployment claims is 404 whatever the verb", logical, got, anyMethod)
		}
		integration, _ := properties["Integration"].(map[string]any)
		if got := integration["Type"]; got != "MOCK" {
			t.Errorf("%s integrates over %v, want MOCK; nothing backs the 404", logical, got)
		}
		templates, _ := integration["RequestTemplates"].(map[string]any)
		for _, contentType := range notFoundContentTypes {
			if templates[contentType] == nil {
				t.Errorf("%s maps %v, want %q among them; an unmapped content type is answered 415 instead of the 404 this API exists to serve", logical, slices.Sorted(maps.Keys(templates)), contentType)
			}
		}
		responses, _ := integration["IntegrationResponses"].([]any)
		if len(responses) != 1 {
			t.Fatalf("%s declares %d integration responses, want one", logical, len(responses))
		}
		answer, _ := responses[0].(map[string]any)
		if got := answer["StatusCode"]; got != "404" {
			t.Errorf("%s answers %v, want 404", logical, got)
		}
		headers, _ := answer["ResponseParameters"].(map[string]any)
		if got := headers[edgeHeaderParameter]; got != "'"+edgeHeaderValue+"'" {
			t.Errorf("%s sets %s = %v, want 'api-gateway'", logical, EdgeHeader, got)
		}
		body, _ := answer["ResponseTemplates"].(map[string]any)
		if got := body["application/json"]; got != `{"message":"Not Found"}` {
			t.Errorf("%s answers with %v, want the JSON body every 404 carries", logical, got)
		}
		declared, _ := properties["MethodResponses"].([]any)
		if len(declared) != 1 {
			t.Errorf("%s declares %d method responses, want the one the integration response fills", logical, len(declared))
		}
	}

	deployment := resources[deploymentOf(t, resources)]
	assertSet(t, "what the deployment waits on", toStrings(deployment["DependsOn"]), []string{
		"EdgeNotFoundRootMethod", "EdgeNotFoundProxyMethod",
	})
}

func deploymentOf(t *testing.T, resources map[string]map[string]any) string {
	t.Helper()
	for id, resource := range resources {
		if resource["Type"] == "AWS::ApiGateway::Deployment" {
			return id
		}
	}
	t.Fatal("the fragment declares no deployment, so the 404 responder's methods are never published")
	return ""
}

func TestAChangedResponderIsPublishedRatherThanLeftOnTheOldDeployment(t *testing.T) {
	resources := coreResources(t, edge.ClassProduction)
	deployment := deploymentOf(t, resources)

	stage, _ := resources["EdgeNotFoundStage"]["Properties"].(map[string]any)
	if got := stage["DeploymentId"]; got != deployment {
		t.Errorf("the stage serves %v, want %s; a stage pointed elsewhere serves whatever was published last", got, deployment)
	}

	held := notFoundContentTypes
	t.Cleanup(func() { notFoundContentTypes = held })
	notFoundContentTypes = append(slices.Clone(held), "application/xml")

	moved := deploymentOf(t, coreResources(t, edge.ClassProduction))
	if moved == deployment {
		t.Errorf("a changed responder kept deployment %s, so the stage goes on serving the methods published before the change", deployment)
	}
}

func TestTheInvokeRoleReachesOnlyThisAccountsFunctionsAndTheAssetBucket(t *testing.T) {
	t.Parallel()

	resources := coreResources(t, edge.ClassProduction)
	properties, _ := resources["EdgeInvokeRole"]["Properties"].(map[string]any)

	trust, _ := properties["AssumeRolePolicyDocument"].(map[string]any)
	statements, _ := trust["Statement"].([]any)
	if len(statements) != 1 {
		t.Fatalf("the trust policy carries %d statements, want one", len(statements))
	}
	principal, _ := statements[0].(map[string]any)["Principal"].(map[string]any)
	if got := principal["Service"]; got != "apigateway.amazonaws.com" {
		t.Errorf("the role is assumed by %v, want apigateway.amazonaws.com", got)
	}

	policies, _ := properties["Policies"].([]any)
	if len(policies) != 1 {
		t.Fatalf("the role carries %d inline policies, want one", len(policies))
	}
	inline, _ := policies[0].(map[string]any)
	if got := inline["PolicyName"]; got != invokePolicyName {
		t.Errorf("PolicyName = %v, want %q", got, invokePolicyName)
	}
	document, _ := inline["PolicyDocument"].(map[string]any)
	granted, _ := document["Statement"].([]any)
	if len(granted) != 2 {
		t.Fatalf("the invoke policy grants %d statements, want the function invoke and the asset read", len(granted))
	}
	for _, statement := range granted {
		grant, _ := statement.(map[string]any)
		if grant["Resource"] == "*" {
			t.Errorf("the invoke policy grants %v on everything; scope it to what the edge fronts", grant["Action"])
		}
	}
	rendered := newWorld().edge().CoreStack(string(edge.ClassProduction)).Resources
	if !strings.Contains(rendered, "arn:aws:lambda:${AWS::Region}:${AWS::AccountId}:function:*") {
		t.Errorf("the invoke policy = %s, want lambda:InvokeFunction scoped to this account's functions in this region", rendered)
	}
	if !strings.Contains(rendered, "${AssetBucket.Arn}/*") {
		t.Errorf("the invoke policy = %s, want it to read the asset bucket the same stack raises", rendered)
	}
}

func TestCoreStackNamesTheOutputsTheEdgeReadsBack(t *testing.T) {
	t.Parallel()

	outputs := coreSection(t, "Outputs", newWorld().edge().CoreStack(string(edge.ClassProduction)).Outputs)
	assertSet(t, "the outputs the fragment carries", slices.Collect(maps.Keys(outputs)), []string{
		OutputInvokeRoleARN, OutputNotFoundAPIID,
	})
	for key, output := range outputs {
		if output["Description"] == "" || output["Value"] == nil {
			t.Errorf("output %s = %v, want a description and a value", key, output)
		}
	}
}

func TestTheEdgeIsACoreFront(t *testing.T) {
	t.Parallel()

	var front any = newWorld().edge()
	if _, ok := front.(bootstrap.CoreFront); !ok {
		t.Fatal("the edge does not fill CoreFront, so nothing it needs reaches the core stack or the change plan")
	}
}

func toStrings(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.(string))
	}
	return out
}
