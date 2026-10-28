package ports

import (
	"net/http"
	"testing"

	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
)

const signedJWT = "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJ3ZWJAcC5pYW0uZ3NlcnZpY2VhY2NvdW50LmNvbSJ9.c2lnbmVkLWJ5LWdvb2dsZQ"

var credentialExchanges = []tracingtest.Exchange{
	{
		Name: "a Google service account's JWT-bearer grant", Method: http.MethodPost, URL: "https://oauth2.googleapis.com/token",
		RequestType: "application/x-www-form-urlencoded", RequestBody: "grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Ajwt-bearer&assertion=" + signedJWT,
		ResponseType: "application/json; charset=utf-8", ResponseBody: `{"access_token":"ya29.c.b0AaekJwt","expires_in":3599,"token_type":"Bearer"}`,
		Secrets: []string{signedJWT, "ya29.c.b0AaekJwt"},
	},
	{
		Name: "a Google service account's ID token grant", Method: http.MethodPost, URL: "https://oauth2.googleapis.com/token",
		RequestType: "application/x-www-form-urlencoded", RequestBody: "grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Ajwt-bearer&assertion=" + signedJWT,
		ResponseType: "application/json", ResponseBody: `{"id_token":"` + signedJWT + `"}`,
		Secrets: []string{signedJWT},
	},
	{
		Name: "an STS token exchange", Method: http.MethodPost, URL: "https://sts.googleapis.com/v1/token",
		RequestType:  "application/x-www-form-urlencoded",
		RequestBody:  "grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Atoken-exchange&subject_token=ghOidcSubject&subject_token_type=urn%3Aietf%3Aparams%3Aoauth%3Atoken-type%3Ajwt&actor_token=ghOidcActor",
		ResponseType: "application/json", ResponseBody: `{"access_token":"ya29.d.stsFederated","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","token_type":"Bearer","expires_in":3600}`,
		Secrets: []string{"ghOidcSubject", "ghOidcActor", "ya29.d.stsFederated"},
	},
	{
		Name: "a metadata server access token", Method: http.MethodGet, URL: "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token",
		ResponseType: "application/json", ResponseBody: `{"access_token":"ya29.m.metadata","expires_in":3599,"token_type":"Bearer"}`,
		Secrets: []string{"ya29.m.metadata"},
	},
	{
		Name: "a metadata server ID token", Method: http.MethodGet, URL: "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/identity?audience=https://web.run.app",
		ResponseType: "text/html; charset=utf-8", ResponseBody: signedJWT,
		Secrets: []string{signedJWT},
	},
	{
		Name: "an IAM credentials access token", Method: http.MethodPost, URL: "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/web@p.iam.gserviceaccount.com:generateAccessToken",
		RequestType: "application/json", RequestBody: `{"scope":["https://www.googleapis.com/auth/cloud-platform"]}`,
		ResponseType: "application/json; charset=UTF-8", ResponseBody: `{"accessToken":"ya29.iam.impersonated","expireTime":"2026-10-27T13:00:00Z"}`,
		Secrets: []string{"ya29.iam.impersonated"},
	},
	{
		Name: "an IAM credentials ID token", Method: http.MethodPost, URL: "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/web@p.iam.gserviceaccount.com:generateIdToken",
		RequestType: "application/json", RequestBody: `{"audience":"https://web.run.app","includeEmail":true}`,
		ResponseType: "application/json", ResponseBody: `{"token":"` + signedJWT + `"}`,
		Secrets: []string{signedJWT},
	},
	{
		Name: "an IAM credentials signed JWT", Method: http.MethodPost, URL: "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/web@p.iam.gserviceaccount.com:signJwt",
		RequestType: "application/json", RequestBody: `{"payload":"{\"iss\":\"web@p.iam.gserviceaccount.com\"}"}`,
		ResponseType: "application/json", ResponseBody: `{"keyId":"k1","signedJwt":"` + signedJWT + `"}`,
		Secrets: []string{signedJWT},
	},
	{
		Name: "a Secret Manager version read", Method: http.MethodGet, URL: "https://secretmanager.googleapis.com/v1/projects/acme/secrets/db/versions/latest:access",
		ResponseType: "application/json", ResponseBody: `{"name":"projects/acme/secrets/db/versions/3","payload":{"data":"cG9zdGdyZXM6Ly9odW50ZXIy"}}`,
		Secrets: []string{"cG9zdGdyZXM6Ly9odW50ZXIy"},
	},
	{
		Name: "a Cloud Run service carrying its env", Method: http.MethodPost, URL: "https://europe-west1-run.googleapis.com/v2/projects/acme/locations/europe-west1/services?serviceId=web",
		RequestType: "application/json", RequestBody: `{"template":{"containers":[{"env":[{"name":"STRIPE_KEY","value":"sk_live_runEnv"}]}]}}`,
		ResponseType: "application/json", ResponseBody: `{"error":{"code":400,"message":"sk_live_runEnv is not a valid value"}}`,
		Secrets: []string{"sk_live_runEnv"},
	},
	{
		Name: "a Cloud SQL user's password", Method: http.MethodPost, URL: "https://sqladmin.googleapis.com/v1/projects/acme/instances/db/users",
		RequestType: "application/json", RequestBody: `{"name":"app","password":"sqlPassw0rd"}`,
		ResponseType: "application/json", ResponseBody: `{"error":{"code":409}}`,
		Secrets: []string{"sqlPassw0rd"},
	},
	{
		Name: "an object's contents read through the JSON API", Method: http.MethodGet, URL: "https://storage.googleapis.com/storage/v1/b/acme-env/o/web.env?alt=media",
		ResponseType: "text/plain", ResponseBody: "API_KEY=objectSecret",
		Secrets: []string{"objectSecret"},
	},
	{
		Name: "an object's contents uploaded", Method: http.MethodPost, URL: "https://storage.googleapis.com/upload/storage/v1/b/acme-env/o?uploadType=media&name=web.env",
		RequestType: "text/plain", RequestBody: "API_KEY=uploadedSecret",
		ResponseType: "application/json", ResponseBody: `{"error":{"code":403}}`,
		Secrets: []string{"uploadedSecret"},
	},
	{
		Name: "an object's contents read through the XML API", Method: http.MethodGet, URL: "https://storage.googleapis.com/acme-env/web.env",
		ResponseType: "text/plain", ResponseBody: "API_KEY=xmlSecret",
		Secrets: []string{"xmlSecret"},
	},
	{
		Name: "an object's contents read from an emulator", Method: http.MethodGet, URL: "http://127.0.0.1:4443/storage/v1/b/acme-env/o/web.env?alt=media",
		ResponseType: "text/plain", ResponseBody: "API_KEY=emulatedSecret",
		Secrets: []string{"emulatedSecret"},
	},
	{
		Name: "an account sign-in", Method: http.MethodPost, URL: "https://accounts.google.com/o/oauth2/revoke",
		RequestType: "application/x-www-form-urlencoded", RequestBody: "token=ya29.revokedAccess",
		ResponseType: "application/json", ResponseBody: `{"error":"invalid_token"}`,
		Secrets: []string{"ya29.revokedAccess"},
	},
	{
		Name: "an IAM credentials signed blob", Method: http.MethodPost, URL: "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/web@p.iam.gserviceaccount.com:signBlob",
		RequestType: "application/json", RequestBody: `{"payload":"aGVsbG8="}`,
		ResponseType: "application/json", ResponseBody: `{"keyId":"k1","signedBlob":"c2lnbmVkQmxvYkJ5dGVz"}`,
		Secrets: []string{"c2lnbmVkQmxvYkJ5dGVz"},
	},
}

func TestAGoogleCallCarryingASecretIsTracedWithoutItsBodiesOrTheSecretsTheyCarry(t *testing.T) {
	for _, exchange := range credentialExchanges {
		t.Run(exchange.Name, func(t *testing.T) {
			tracingtest.CheckRecordedWithoutBodies(t, exchange, tracingtest.SendRefusedThrough(t, exchange, NewGoogleTransport))
		})
	}
}

func TestAFailedGoogleAPICallCarryingNoSecretKeepsItsBodies(t *testing.T) {
	for _, exchange := range []tracingtest.Exchange{
		{
			Method: http.MethodPost, URL: "https://pubsub.googleapis.com/v1/projects/acme/topics/orders:publish",
			RequestType: "application/json", RequestBody: `{"messages":[{"attributes":{"order":"o-17"}}]}`,
			ResponseType: "application/json", ResponseBody: `{"error":{"code":400}}`,
		},
		{
			Method: http.MethodGet, URL: "https://storage.googleapis.com/storage/v1/b/acme-env/o?prefix=web",
			ResponseType: "application/json", ResponseBody: `{"error":{"code":404}}`,
		},
	} {
		t.Run(exchange.URL, func(t *testing.T) {
			span := tracingtest.SendRefusedThrough(t, exchange, NewGoogleTransport)
			if body := tracingtest.Attributes(span)["http.response.body"]; body.AsString() != exchange.ResponseBody {
				t.Errorf("http.response.body = %q, want the API's refusal", body.AsString())
			}
		})
	}
}
