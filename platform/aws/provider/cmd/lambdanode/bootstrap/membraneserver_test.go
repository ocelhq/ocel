package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/channel"
	"github.com/ocelhq/ocel/pkg/naming"
	bucketsv1 "github.com/ocelhq/ocel/pkg/proto/buckets/v1"
	"github.com/ocelhq/ocel/pkg/proto/buckets/v1/bucketsv1connect"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/vars/live"
)

func membraneEnvValue(t *testing.T, env []string, key string) string {
	t.Helper()
	for _, entry := range env {
		if name, value, ok := strings.Cut(entry, "="); ok && name == key {
			return value
		}
	}
	t.Fatalf("env %q carries no %s", env, key)
	return ""
}

var testSessionPrefix = naming.SessionKeyPrefix("shop", "prod")

func TestServeMembrane(t *testing.T) {
	t.Run("a deployment whose links all go direct serves nothing", func(t *testing.T) {
		env, served, err := serveMembrane(context.Background(), []live.Link{{Name: "db--main", Type: linksv1.LinkType_LINK_TYPE_POSTGRES}}, "state", testSessionPrefix)
		if err != nil {
			t.Fatalf("serveMembrane: %v", err)
		}
		if env != nil || served != nil {
			t.Fatalf("serveMembrane = %q, want no membrane for a deployment that reaches postgres directly", env)
		}
	})

	t.Run("a bucket with nowhere to keep its sessions fails by name", func(t *testing.T) {
		_, _, err := serveMembrane(context.Background(), []live.Link{{Name: "bucket--uploads", Type: linksv1.LinkType_LINK_TYPE_BUCKET}}, "", testSessionPrefix)
		if err == nil || !strings.Contains(err.Error(), stateTableEnvVar) {
			t.Fatalf("serveMembrane err = %v, want it to name %s", err, stateTableEnvVar)
		}
	})

	t.Run("a bucket whose sessions have no key scope refuses to serve", func(t *testing.T) {
		_, _, err := serveMembrane(context.Background(), []live.Link{{Name: "bucket--uploads", Type: linksv1.LinkType_LINK_TYPE_BUCKET}}, "state", "")
		if err == nil || !strings.Contains(err.Error(), sessionPrefixEnvVar) {
			t.Fatalf("serveMembrane err = %v, want it to name %s", err, sessionPrefixEnvVar)
		}
	})

	t.Run("a bucket is served in-process, reachable only with the token the child is handed", func(t *testing.T) {
		t.Setenv("AWS_REGION", "us-east-1")
		t.Setenv("AWS_ACCESS_KEY_ID", "test")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "test")

		env, served, err := serveMembrane(context.Background(), []live.Link{{Name: "bucket--uploads", Type: linksv1.LinkType_LINK_TYPE_BUCKET}}, "state", testSessionPrefix)
		if err != nil {
			t.Fatalf("serveMembrane: %v", err)
		}
		if served == nil {
			t.Fatal("serveMembrane returned no channel to carry the membrane's terminal error")
		}

		addr := membraneEnvValue(t, env, runtimeAddressEnvVar)
		if !strings.HasPrefix(addr, "http://127.0.0.1:") {
			t.Fatalf("%s = %q, want a loopback address the sandbox alone can reach", runtimeAddressEnvVar, addr)
		}
		token := membraneEnvValue(t, env, channel.SessionTokenEnvVar)
		if token == "" {
			t.Fatalf("%s is empty, so the membrane is open to anything in the sandbox", channel.SessionTokenEnvVar)
		}

		client := bucketsv1connect.NewBucketServiceClient(http.DefaultClient, addr)
		_, err = client.PresignUpload(context.Background(), &bucketsv1.PresignUploadRequest{Bucket: "uploads"})
		var connectErr *connect.Error
		if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeUnauthenticated {
			t.Fatalf("unauthenticated PresignUpload err = %v, want CodeUnauthenticated", err)
		}

		bearer := bucketsv1connect.NewBucketServiceClient(&http.Client{Transport: bearerToken(token)}, addr)
		_, err = bearer.PresignUpload(context.Background(), &bucketsv1.PresignUploadRequest{Bucket: "uploads"})
		if errors.As(err, &connectErr) && connectErr.Code() == connect.CodeUnauthenticated {
			t.Fatalf("PresignUpload with the token exported into the child's environment = %v, want the membrane to answer it", err)
		}
	})
}

type bearerToken string

func (b bearerToken) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", channel.FormatAuthHeader(string(b)))
	return http.DefaultTransport.RoundTrip(req)
}
