package apigateway

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	OutputInvokeRoleARN = "EdgeInvokeRoleArn"
	OutputNotFoundAPIID = "EdgeNotFoundApiId"
)

var notFoundContentTypes = []string{
	"application/json",
	"text/plain",
	"text/html",
	"application/x-www-form-urlencoded",
	"multipart/form-data",
	"application/octet-stream",
}

var _ bootstrap.CoreFront = (*provider)(nil)

func (p *provider) CoreStack(class string) bootstrap.CoreFragment {
	c := edge.Class(class)
	if knownClass(c) != nil {
		return bootstrap.CoreFragment{}
	}
	responder := notFoundAPIResource(c) +
		notFoundProxyResource() +
		notFoundMethodResource("EdgeNotFoundRootMethod", "!GetAtt EdgeNotFoundApi.RootResourceId", rootPath) +
		notFoundMethodResource("EdgeNotFoundProxyMethod", "!Ref EdgeNotFoundProxy", rootPath+proxyPathPart)
	published := notFoundDeploymentID(responder)
	return bootstrap.CoreFragment{
		Resources: invokeRoleResource(c) +
			responder +
			notFoundDeploymentResource(published) +
			notFoundStageResource(published),
		Outputs: edgeOutputs(),
	}
}

func notFoundDeploymentID(responder string) string {
	sum := sha256.Sum256([]byte(responder))
	return "EdgeNotFoundDeployment" + hex.EncodeToString(sum[:6])
}

func invokeRoleResource(class edge.Class) string {
	return fmt.Sprintf(`  EdgeInvokeRole:
    Type: AWS::IAM::Role
    Metadata:
      Description: "The role API Gateway assumes to invoke this account's entry functions and to read a release's static assets out of the asset bucket. Every REST API Ocel raises in the %s class names it, so deleting it blanks every deployment this bootstrap fronts."
    Properties:
      RoleName: %s
      Description: "API Gateway assumes this role to invoke Ocel's entry functions and read a release's static assets."
      AssumeRolePolicyDocument:
        Version: '2012-10-17'
        Statement:
          - Effect: Allow
            Principal:
              Service: apigateway.amazonaws.com
            Action: sts:AssumeRole
      Policies:
        - PolicyName: %s
          PolicyDocument:
            Version: '2012-10-17'
            Statement:
              - Effect: Allow
                Action: lambda:InvokeFunction
                Resource: !Sub 'arn:aws:lambda:${AWS::Region}:${AWS::AccountId}:function:*'
              - Effect: Allow
                Action: s3:GetObject
                Resource: !Sub '${AssetBucket.Arn}/*'
`, class, invokeRoleName(class), invokePolicyName)
}

func notFoundAPIResource(class edge.Class) string {
	return fmt.Sprintf(`  EdgeNotFoundApi:
    Type: AWS::ApiGateway::RestApi
    Metadata:
      Description: "The REST API every host pointed at this account that no %s deployment claims is answered 404 from. The preview wildcard's catch-all routing rule points here."
    Properties:
      Name: %s
      Description: "Ocel answers 404 from here for every host pointed at this account that no deployment claims."
      EndpointConfiguration:
        Types:
          - REGIONAL
`, class, notFoundAPIName(class))
}

func notFoundProxyResource() string {
	return fmt.Sprintf(`  EdgeNotFoundProxy:
    Type: AWS::ApiGateway::Resource
    Metadata:
      Description: "The catch-all path under the 404 responder, so a request for any path and not only the root is answered rather than refused."
    Properties:
      RestApiId: !Ref EdgeNotFoundApi
      ParentId: !GetAtt EdgeNotFoundApi.RootResourceId
      PathPart: '%s'
`, proxyPathPart)
}

func notFoundMethodResource(logical, resourceID, path string) string {
	var templates strings.Builder
	for _, contentType := range notFoundContentTypes {
		fmt.Fprintf(&templates, "          '%s': '{\"statusCode\": 404}'\n", contentType)
	}
	return fmt.Sprintf(`  %s:
    Type: AWS::ApiGateway::Method
    Metadata:
      Description: "Answers every method on %s with a mocked 404 carrying the %s header, so a host no deployment claims is told so by Ocel rather than by API Gateway."
    Properties:
      RestApiId: !Ref EdgeNotFoundApi
      ResourceId: %s
      HttpMethod: %s
      AuthorizationType: NONE
      Integration:
        Type: MOCK
        PassthroughBehavior: WHEN_NO_TEMPLATES
        RequestTemplates:
%s        IntegrationResponses:
          - StatusCode: '404'
            ResponseParameters:
              %s: "'%s'"
            ResponseTemplates:
              'application/json': '{"message":"Not Found"}'
      MethodResponses:
        - StatusCode: '404'
          ResponseParameters:
            %s: true
`, logical, path, EdgeHeader, resourceID, anyMethod, templates.String(), edgeHeaderParameter, edgeHeaderValue, edgeHeaderParameter)
}

func notFoundDeploymentResource(logical string) string {
	return fmt.Sprintf(`  %s:
    Type: AWS::ApiGateway::Deployment
    DependsOn:
      - EdgeNotFoundRootMethod
      - EdgeNotFoundProxyMethod
    Metadata:
      Description: "Publishes the 404 responder's two methods; without it the API stands but serves nothing. Named for what it publishes, so a changed responder is published rather than left behind."
    Properties:
      RestApiId: !Ref EdgeNotFoundApi
`, logical)
}

func notFoundStageResource(deployment string) string {
	return fmt.Sprintf(`  EdgeNotFoundStage:
    Type: AWS::ApiGateway::Stage
    Metadata:
      Description: "The stage routing rules name when they send an unclaimed host to the 404 responder; every Ocel REST API serves from a stage of this name."
    Properties:
      RestApiId: !Ref EdgeNotFoundApi
      DeploymentId: !Ref %s
      StageName: %s
`, deployment, stageName)
}

func edgeOutputs() string {
	return fmt.Sprintf(`  %s:
    Description: "ARN of the role API Gateway assumes to invoke this account's entry functions and read a release's static assets. Every deploy reads it and names it on the integrations it raises."
    Value: !GetAtt EdgeInvokeRole.Arn
  %s:
    Description: "Id of the REST API answering 404 for every host pointed at this account that no deployment claims, which the preview wildcard's catch-all routing rule points at."
    Value: !Ref EdgeNotFoundApi
`, OutputInvokeRoleARN, OutputNotFoundAPIID)
}
