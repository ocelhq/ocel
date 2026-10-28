package sdkconfig

import (
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/smithy-go/middleware"

	"github.com/ocelhq/ocel/pkg/tracing"
	"github.com/ocelhq/ocel/pkg/tracing/content"
)

var servicesWithSecretContent = map[string]bool{
	"CloudFront KeyValueStore": true,
	"DynamoDB":                 true,
	"ECR":                      true,
	"ECS":                      true,
	"IAM":                      true,
	"KMS":                      true,
	"Lambda":                   true,
	"RDS Data":                 true,
	"S3":                       true,
	"Secrets Manager":          true,
	"Signin":                   true,
	"SSM":                      true,
	"SSO":                      true,
	"SSO OIDC":                 true,
	"STS":                      true,
}

type tracedClient struct {
	transport http.RoundTripper
}

func NewTracedClient(client aws.HTTPClient) aws.HTTPClient {
	switch sender := client.(type) {
	case tracedClient:
		return client
	case nil:
		client = awshttp.NewBuildableClient()
	case *http.Client:
		if _, traced := sender.Transport.(*tracing.Transport); traced {
			return tracedClient{transport: clientTransport{client}}
		}
	}
	return tracedClient{transport: tracing.NewTransport(clientTransport{client})}
}

func (c tracedClient) Do(req *http.Request) (*http.Response, error) {
	if service := middleware.GetServiceID(req.Context()); service == "" || servicesWithSecretContent[service] {
		req = req.WithContext(content.Omit(req.Context()))
	}
	return c.transport.RoundTrip(req)
}

type clientTransport struct {
	client aws.HTTPClient
}

func (t clientTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.client.Do(req)
}
