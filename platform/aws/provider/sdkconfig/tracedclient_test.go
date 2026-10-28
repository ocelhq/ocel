package sdkconfig

import (
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go/middleware"

	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
)

const (
	signedJWT    = "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJ3ZWJAcC5pYW0uZ3NlcnZpY2VhY2NvdW50LmNvbSJ9.c2lnbmVkLWJ5LWdvb2dsZQ"
	awsSecretKey = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	awsSession   = "IQoJb3JpZ2luX2VjEOr//////////wEaCXVzLWVhc3QtMSJHMEUCIQ"
)

var credentialExchanges = []tracingtest.Exchange{
	{
		Name: "an IMDSv2 session token", Method: http.MethodPut, URL: "http://169.254.169.254/latest/api/token",
		ResponseType: "text/plain", ResponseBody: "AQAEAOx4MhUQVN0mFq6wKsK2Z8YxHH4yzxQ8d0Xwv6s1rXg==",
		Secrets: []string{"AQAEAOx4MhUQVN0mFq6wKsK2Z8YxHH4yzxQ8d0Xwv6s1rXg"},
	},
	{
		Name: "IMDS role credentials over IPv6", Method: http.MethodGet, URL: "http://[fd00:ec2::254]/latest/meta-data/iam/security-credentials/web-role",
		ResponseType: "text/plain",
		ResponseBody: `{"Code":"Success","Type":"AWS-HMAC","AccessKeyId":"ASIAEXAMPLEKEYID0001","SecretAccessKey":"` + awsSecretKey + `","Token":"` + awsSession + `","Expiration":"2026-10-27T13:00:00Z"}`,
		Secrets:      []string{awsSecretKey, awsSession},
	},
	{
		Name: "ECS container credentials", Method: http.MethodGet, URL: "http://169.254.170.2/v2/credentials/7f3e",
		ResponseType: "application/json",
		ResponseBody: `{"RoleArn":"arn:aws:iam::1:role/web","AccessKeyId":"ASIAEXAMPLEKEYID0002","SecretAccessKey":"` + awsSecretKey + `","Token":"` + awsSession + `","Expiration":"2026-10-27T13:00:00Z"}`,
		Secrets:      []string{awsSecretKey, awsSession},
	},
	{
		Name: "an AWS STS web identity grant", Method: http.MethodPost, URL: "https://sts.eu-west-1.amazonaws.com/",
		RequestType: "application/x-www-form-urlencoded", RequestBody: "Action=AssumeRoleWithWebIdentity&RoleArn=arn%3Aaws%3Aiam%3A%3A1%3Arole%2Fweb&RoleSessionName=ocel&Version=2011-06-15&WebIdentityToken=" + signedJWT,
		ResponseType: "text/xml",
		ResponseBody: "<AssumeRoleWithWebIdentityResponse><AssumeRoleWithWebIdentityResult><Credentials><AccessKeyId>ASIAEXAMPLEKEYID0004</AccessKeyId><SecretAccessKey>" + awsSecretKey + "</SecretAccessKey><SessionToken>" + awsSession + "</SessionToken></Credentials></AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>",
		Secrets:      []string{signedJWT, awsSecretKey, awsSession},
	},
	{
		Name: "AWS SSO role credentials", Method: http.MethodGet, URL: "https://portal.sso.eu-west-1.amazonaws.com/federation/credentials?account_id=1&role_name=web",
		ResponseType: "application/json",
		ResponseBody: `{"roleCredentials":{"accessKeyId":"ASIAEXAMPLEKEYID0005","secretAccessKey":"` + awsSecretKey + `","sessionToken":"` + awsSession + `","expiration":1793000000000}}`,
		Secrets:      []string{awsSecretKey, awsSession},
	},
	{
		Name: "an AWS SSO OIDC refresh", Method: http.MethodPost, URL: "https://oidc.eu-west-1.amazonaws.com/token",
		RequestType: "application/json", RequestBody: `{"clientId":"c1","clientSecret":"ssoClientSecret","grantType":"refresh_token","refreshToken":"aorvOldRefresh","codeVerifier":"ssoCodeVerifier"}`,
		ResponseType: "application/json", ResponseBody: `{"accessToken":"aoaSsoAccess","expiresIn":3600,"refreshToken":"aorvNewRefresh","tokenType":"Bearer"}`,
		Secrets: []string{"ssoClientSecret", "aorvOldRefresh", "ssoCodeVerifier", "aoaSsoAccess", "aorvNewRefresh"},
	},
}

var sentBy = map[string]string{
	"an IMDSv2 session token":         "",
	"IMDS role credentials over IPv6": "",
	"ECS container credentials":       "",
	"an AWS STS web identity grant":   "STS",
	"AWS SSO role credentials":        "SSO",
	"an AWS SSO OIDC refresh":         "SSO OIDC",
}

type serviceClient struct {
	client  aws.HTTPClient
	service string
}

func (c serviceClient) RoundTrip(req *http.Request) (*http.Response, error) {
	if c.service != "" {
		req = req.WithContext(middleware.WithServiceID(req.Context(), c.service))
	}
	return c.client.Do(req)
}

func sentByService(service string) func(http.RoundTripper) http.RoundTripper {
	return func(upstream http.RoundTripper) http.RoundTripper {
		return serviceClient{client: NewTracedClient(&http.Client{Transport: upstream}), service: service}
	}
}

func TestAnAWSCredentialExchangeIsTracedWithoutItsBodiesOrTheCredentialsTheyCarry(t *testing.T) {
	for _, exchange := range credentialExchanges {
		t.Run(exchange.Name, func(t *testing.T) {
			service, named := sentBy[exchange.Name]
			if !named {
				t.Fatalf("no client named for %q", exchange.Name)
			}
			tracingtest.CheckRecordedWithoutBodies(t, exchange, tracingtest.SendRefusedThrough(t, exchange, sentByService(service)))
		})
	}
}

func TestAFailedAWSAPICallWhoseBodiesCarryNoSecretKeepsThem(t *testing.T) {
	span := tracingtest.SendRefusedThrough(t, tracingtest.Exchange{
		Method: http.MethodPost, URL: "https://acm.us-east-1.amazonaws.com/",
		RequestType: "application/x-amz-json-1.1", RequestBody: `{"DomainName":"acme.dev"}`,
		ResponseType: "application/x-amz-json-1.1", ResponseBody: `{"__type":"ValidationException"}`,
	}, sentByService("ACM"))
	if body := tracingtest.Attributes(span)["http.response.body"]; body.AsString() != `{"__type":"ValidationException"}` {
		t.Errorf("http.response.body = %q, want the API's refusal", body.AsString())
	}
}
