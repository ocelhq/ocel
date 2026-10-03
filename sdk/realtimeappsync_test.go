package ocel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const appSyncVectorBody = `{"channel":"/app/orders/o1","events":["{\"v\":1}"]}`

func TestAnAppSyncPublishIsSignedAsTheAWSSDKSignsIt(t *testing.T) {
	at := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		creds awsCredentials
		want  string
	}{
		{
			"long-lived keys",
			awsCredentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"},
			"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261003/eu-west-1/appsync/aws4_request, SignedHeaders=content-type;host;x-amz-date, Signature=6c8f1bf8968ad3ec649127bc436717dd96a143637d076bfe6013567da6855ae1",
		},
		{
			"a role's session",
			awsCredentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", SessionToken: "session-token"},
			"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261003/eu-west-1/appsync/aws4_request, SignedHeaders=content-type;host;x-amz-date;x-amz-security-token, Signature=aa66eda43423cb89f230afcbf748369d3decaa2a23ef192d0b4a3d41087c747b",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := signAppSyncPublish("abc123.appsync-api.eu-west-1.amazonaws.com", []byte(appSyncVectorBody), tc.creds, "eu-west-1", at)
			if got := headers.Get("Authorization"); got != tc.want {
				t.Errorf("Authorization = %q, want %q", got, tc.want)
			}
			if got := headers.Get("X-Amz-Date"); got != "20261003T120000Z" {
				t.Errorf("X-Amz-Date = %q, want 20261003T120000Z", got)
			}
			if got := headers.Get("X-Amz-Security-Token"); got != tc.creds.SessionToken {
				t.Errorf("X-Amz-Security-Token = %q, want %q", got, tc.creds.SessionToken)
			}
		})
	}
}

func TestAnAppSyncHostNamesItsRegion(t *testing.T) {
	t.Setenv("AWS_REGION", "us-west-2")
	for host, want := range map[string]string{
		"abc123.appsync-api.eu-west-1.amazonaws.com":     "eu-west-1",
		"abc123.appsync-api.cn-north-1.amazonaws.com.cn": "cn-north-1",
		"127.0.0.1:8443":       "us-west-2",
		"realtime.example.com": "us-west-2",
	} {
		if got := findAppSyncRegion(host); got != want {
			t.Errorf("findAppSyncRegion(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestAWSCredentialsComeFromTheEnvironmentFirst(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDENV")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AWS_SESSION_TOKEN", "token")
	t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", "http://127.0.0.1:1/never")
	creds, err := readAWSCredentials(context.Background())
	if err != nil || creds != (awsCredentials{AccessKeyID: "AKIDENV", SecretAccessKey: "secret", SessionToken: "token"}) {
		t.Errorf("readAWSCredentials() = %+v, %v, want the environment's keys", creds, err)
	}
}

func TestAWSCredentialsOfAContainerAreReadFromItsEndpointAndKeptUntilNearExpiry(t *testing.T) {
	var reads atomic.Int32
	expires := time.Now().Add(time.Hour).UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if got := r.Header.Get("Authorization"); got != "container-token" {
			t.Errorf("the credentials endpoint was asked with Authorization %q, want the container's token", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"AccessKeyId":     "ASIACONTAINER",
			"SecretAccessKey": "container-secret",
			"Token":           "container-session",
			"Expiration":      expires.Format(time.RFC3339),
		})
	}))
	t.Cleanup(server.Close)
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "")
	t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", server.URL+"/v2/credentials")
	t.Setenv("AWS_CONTAINER_AUTHORIZATION_TOKEN", "container-token")
	forgetContainerCredentials()
	t.Cleanup(forgetContainerCredentials)

	for range 3 {
		creds, err := readAWSCredentials(context.Background())
		if err != nil || creds.AccessKeyID != "ASIACONTAINER" || creds.SessionToken != "container-session" {
			t.Fatalf("readAWSCredentials() = %+v, %v, want the container's role session", creds, err)
		}
	}
	if reads.Load() != 1 {
		t.Errorf("the endpoint was read %d times, want once while the session is fresh", reads.Load())
	}
}

func TestAWSCredentialsThatCannotBeFoundAreSaidSo(t *testing.T) {
	for _, name := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_CONTAINER_CREDENTIALS_FULL_URI"} {
		t.Setenv(name, "")
	}
	forgetContainerCredentials()
	if _, err := readAWSCredentials(context.Background()); err == nil || !strings.Contains(err.Error(), "AWS_ACCESS_KEY_ID") {
		t.Errorf("readAWSCredentials() = %v, want the missing credentials named", err)
	}
}

type recordedHosts struct{ hosts []string }

func (r *recordedHosts) RoundTrip(req *http.Request) (*http.Response, error) {
	r.hosts = append(r.hosts, req.URL.Host)
	return nil, errors.New("unreachable")
}

func askContainerCredentialsEndpoints(t *testing.T, endpoints []string) ([]string, []error) {
	t.Helper()
	recorded := &recordedHosts{}
	realtimeHTTPClient = &http.Client{Transport: recorded}
	t.Cleanup(func() { realtimeHTTPClient = http.DefaultClient })
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "")
	t.Setenv("AWS_CONTAINER_AUTHORIZATION_TOKEN", "container-token")
	t.Cleanup(forgetContainerCredentials)
	var errs []error
	for _, endpoint := range endpoints {
		forgetContainerCredentials()
		t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", endpoint)
		_, err := readAWSCredentials(context.Background())
		errs = append(errs, err)
	}
	return recorded.hosts, errs
}

func TestAContainerCredentialsEndpointOffTheHostOverPlainHTTPIsNeverSentTheToken(t *testing.T) {
	endpoints := []string{"http://credentials.example.com/v2", "http://10.0.0.5/v2", "http://169.254.169.254/latest", "http://127.0.0.1.example.com/v2", "ftp://127.0.0.1/v2"}
	asked, errs := askContainerCredentialsEndpoints(t, endpoints)
	for i, err := range errs {
		if err == nil || !strings.Contains(err.Error(), "AWS_CONTAINER_CREDENTIALS_FULL_URI") {
			t.Errorf("readAWSCredentials() with %s = %v, want a refusal naming AWS_CONTAINER_CREDENTIALS_FULL_URI", endpoints[i], err)
		}
	}
	if len(asked) != 0 {
		t.Errorf("asked %v, want no refused endpoint asked", asked)
	}
}

func TestAContainerCredentialsEndpointOnLoopbackTheContainerAddressesOrHTTPSIsAsked(t *testing.T) {
	endpoints := []string{"http://localhost:9000/v2", "http://127.0.0.1/v2", "http://127.8.9.10/v2", "http://[::1]/v2", "http://169.254.170.2/v2", "http://169.254.170.23/v1", "http://[fd00:ec2::23]/v1", "https://credentials.example.com/v2"}
	asked, _ := askContainerCredentialsEndpoints(t, endpoints)
	if len(asked) != len(endpoints) {
		t.Errorf("asked %v, want every one of %v", asked, endpoints)
	}
}
