package aws

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/control"
	"github.com/ocelhq/ocel/platform/aws/provider/control/controltest"
	awsresources "github.com/ocelhq/ocel/platform/aws/provider/resources"
)

func TestStateBackendURLIncludesTheEndpointTheAccountIsReachedOn(t *testing.T) {
	t.Setenv("AWS_ENDPOINT_URL", "")
	t.Setenv("AWS_ENDPOINT_URL_S3", "")
	if got := stateBackendURL("state", "hello"); got != "s3://state/hello" {
		t.Fatalf("with no endpoint the backend is %q, want the bare bucket path", got)
	}

	t.Setenv("AWS_ENDPOINT_URL", "http://127.0.0.1:4566")
	got := stateBackendURL("state", "hello")
	want := "s3://state/hello?disableSSL=true&endpoint=127.0.0.1%3A4566&s3ForcePathStyle=true"
	if got != want {
		t.Fatalf("with AWS_ENDPOINT_URL set the backend is\n %q\nwant\n %q", got, want)
	}

	t.Setenv("AWS_ENDPOINT_URL_S3", "https://s3.example.test")
	got = stateBackendURL("state", "hello")
	want = "s3://state/hello?endpoint=s3.example.test&s3ForcePathStyle=true"
	if got != want {
		t.Fatalf("the S3-specific endpoint wins and https keeps TLS; got %q, want %q", got, want)
	}
}

var defaultNamespace = account.Namespace(provider.DefaultNamespace)

func TestTheProviderNamesEveryBindingItsResourcesProvision(t *testing.T) {
	t.Parallel()

	p := NewProvider(control.Options{Region: "us-east-1"}, nil, aws.Config{Region: "us-east-1"}, defaultNamespace)
	if named, provisioned := p.Facts().Bindings, awsresources.Registry(awsresources.Clients{}).Types(); !slices.Equal(named, provisioned) {
		t.Errorf("Facts().Bindings = %v, want %v, the bindings the resource registry provisions", named, provisioned)
	}
}

func TestTheFullProviderRunsItsEngineWhereTheControlAssemblyRefuses(t *testing.T) {
	t.Parallel()

	p := NewProvider(control.Options{Region: "us-east-1"}, nil, unreachableAccount(t), defaultNamespace)
	calls, err := controltest.EngineCalls(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	for name, call := range calls {
		if code, refused := provider.RefusedCode(callRecovered(call)); refused && code == refusal.CodeNeedsRunner {
			t.Errorf("%s refused as needing a runner, want the full provider to run it", name)
		}
	}
}

func TestTheFullProviderSetsEveryHookTheControlAssemblySets(t *testing.T) {
	t.Parallel()

	full := NewProvider(control.Options{Region: "us-east-1"}, nil, aws.Config{Region: "us-east-1"}, defaultNamespace)
	for _, unset := range unsetHooks(reflect.ValueOf(full.Provider.Hooks()), reflect.ValueOf(full.Hooks()), "Hooks()") {
		t.Errorf("%s is set on the control assembly and nil on the full provider", unset)
	}
}

func unsetHooks(control, full reflect.Value, path string) []string {
	var unset []string
	for i := range control.NumField() {
		name := path + "." + control.Type().Field(i).Name
		served, provided := control.Field(i), full.Field(i)
		if served.IsNil() {
			continue
		}
		if provided.IsNil() {
			unset = append(unset, name)
			continue
		}
		if served.Kind() == reflect.Pointer {
			unset = append(unset, unsetHooks(served.Elem(), provided.Elem(), name)...)
		}
	}
	return unset
}

func unreachableAccount(t *testing.T) aws.Config {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(server.Close)
	return aws.Config{
		Region:           "us-east-1",
		BaseEndpoint:     aws.String(server.URL),
		Credentials:      credentials.NewStaticCredentialsProvider("id", "secret", ""),
		RetryMaxAttempts: 1,
	}
}

func callRecovered(call func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panicked: %v", recovered)
		}
	}()
	return call()
}
