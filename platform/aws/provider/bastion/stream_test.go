package bastion_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"go.opentelemetry.io/otel/codes"

	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
)

func TestOpeningASessionIsASpanNamingTheNodeAndTheHostItForwardsTo(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"__type":"TargetNotConnected","message":"not connected"}`))
	}))
	t.Cleanup(server.Close)
	cfg := aws.Config{
		Region:       "eu-west-2",
		BaseEndpoint: aws.String(server.URL),
		Credentials:  credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "example-secret", ""),
		HTTPClient:   sdkconfig.NewTracedClient(nil),
	}

	if _, err := bastion.OpenSession(cfg)(ctx, "ecs:cluster_task_runtime", "db.internal", 5432); err == nil {
		t.Fatal("OpenSession succeeded against a refusing endpoint")
	}

	span := tracingtest.SpanNamed(t, recorded.Ended(), "open bastion session")
	attrs := tracingtest.Attributes(span)
	want := map[string]string{"aws.ssm.target": "ecs:cluster_task_runtime", "server.address": "db.internal", "server.port": "5432"}
	for k, v := range want {
		if attrs[k].String() != v {
			t.Errorf("%s = %q, want %q", k, attrs[k].String(), v)
		}
	}
	if span.Status().Code != codes.Error {
		t.Errorf("status = %v, want an error", span.Status())
	}
	if len(tracingtest.ChildrenOf(recorded.Ended(), span)) == 0 {
		t.Error("the session's StartSession call left no span under open bastion session")
	}
}
